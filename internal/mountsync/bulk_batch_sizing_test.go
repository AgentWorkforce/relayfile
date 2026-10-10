package mountsync

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// legacyBulkWriteRequestSize is the pre-incremental reference: marshal the
// whole request body and measure it.
func legacyBulkWriteRequestSize(files []BulkWriteFile) int64 {
	body := struct {
		Files []BulkWriteFile `json:"files"`
	}{Files: files}
	data, err := json.Marshal(body)
	if err != nil {
		return 0
	}
	return int64(len(data))
}

// legacyChunkOutboxRecords is the O(n^2) reference implementation that
// chunkOutboxRecords replaced. Kept verbatim so the boundary test proves the
// incremental sizer is byte-identical.
func legacyChunkOutboxRecords(records []outboxRecord, maxBytes int64) [][]outboxRecord {
	if len(records) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return [][]outboxRecord{records}
	}
	chunks := make([][]outboxRecord, 0, 1)
	current := make([]outboxRecord, 0, len(records))
	for _, record := range records {
		candidate := append(append([]outboxRecord(nil), current...), record)
		if len(current) > 0 && legacyBulkWriteRequestSize(outboxRecordsAsBulkFiles(candidate)) > maxBytes {
			chunks = append(chunks, append([]outboxRecord(nil), current...))
			current = current[:0]
		}
		current = append(current, record)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// legacyWriteFilesBulkBounds reproduces the pre-incremental WriteFilesBulk
// chunk loop and returns its [start,end) boundaries plus the request size the
// singleton streaming decision used.
func legacyWriteFilesBulkBounds(files []BulkWriteFile, maxBytes int64) []bulkWriteChunkBound {
	var bounds []bulkWriteChunkBound
	for start := 0; start < len(files); {
		end := start + 1
		for end < len(files) && legacyBulkWriteRequestSize(files[start:end+1]) <= maxBytes {
			end++
		}
		bounds = append(bounds, bulkWriteChunkBound{start: start, end: end, size: legacyBulkWriteRequestSize(files[start:end])})
		start = end
	}
	return bounds
}

func legacyChunkPendingBulkWrites(workspaceID string, pending []pendingBulkWrite, maxBytes int64) [][]pendingBulkWrite {
	if len(pending) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return [][]pendingBulkWrite{pending}
	}
	chunks := make([][]pendingBulkWrite, 0, 1)
	current := make([]pendingBulkWrite, 0, len(pending))
	for _, item := range pending {
		candidate := append(append([]pendingBulkWrite(nil), current...), item)
		if len(current) > 0 && legacyBulkWriteRequestSize(bulkWriteFilesForPending(workspaceID, candidate)) > maxBytes {
			chunks = append(chunks, append([]pendingBulkWrite(nil), current...))
			current = current[:0]
		}
		current = append(current, item)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// mixedSizeOutboxCorpus builds records whose JSON encodings vary widely:
// empty, tiny, ~3 KiB code, HTML-escaped characters, U+2028, multibyte
// UTF-8, base64 binaries, symlinks, draft paths with content identities, and
// payloads both just under and well over the 8 MiB request cap.
func mixedSizeOutboxCorpus(seed int64) []outboxRecord {
	rng := rand.New(rand.NewSource(seed))
	code := strings.Repeat("if a < b && c > d { return \"x\" }\n", 100)
	var records []outboxRecord
	add := func(path, content, encoding, kind, target string) {
		i := len(records)
		records = append(records, outboxRecord{
			CommandID:        fmt.Sprintf("mountcmd_%04d", i),
			WorkspaceID:      "ws_sizing",
			RemotePath:       path,
			ContentType:      "text/plain",
			Content:          content,
			Encoding:         encoding,
			Type:             kind,
			Target:           target,
			Mode:             uint32(0o644 + (i%2)*0o111),
			Hash:             fmt.Sprintf("%064x", i),
			ExpectedRevision: []string{"0", "rev_12", ""}[i%3],
		})
	}
	for i := 0; i < 250; i++ {
		switch rng.Intn(10) {
		case 0:
			add(fmt.Sprintf("/repo/empty/%d.txt", i), "", "", "", "")
		case 1:
			add(fmt.Sprintf("/repo/html/%d.html", i), strings.Repeat("<a href=\"&\"> é</a>", 1+rng.Intn(400)), "", "", "")
		case 2:
			raw := make([]byte, 1+rng.Intn(64<<10))
			rng.Read(raw)
			add(fmt.Sprintf("/repo/bin/%d.bin", i), base64.StdEncoding.EncodeToString(raw), "base64", "", "")
		case 3:
			add(fmt.Sprintf("/repo/link/%d", i), "../target", "", remoteTypeSymlink, "../target")
		case 4:
			add(fmt.Sprintf("/slack/channels/C1/messages/factory-create-%d.json", i), `{"text":"draft <&>"}`, "", "", "")
		case 5:
			add(fmt.Sprintf("/repo/big/%d.txt", i), strings.Repeat("y", 256<<10+rng.Intn(1<<20)), "", "", "")
		default:
			add(fmt.Sprintf("/repo/src/%d.go", i), code[:rng.Intn(len(code))], "", "", "")
		}
	}
	// Near-cap and over-cap singletons exercise the 8 MiB edge and the raw
	// streaming fallback.
	add("/repo/huge/near-cap.txt", strings.Repeat("n", int(defaultMaxWritebackBatchBytes)-512), "", "", "")
	add("/repo/huge/over-cap.txt", strings.Repeat("o", int(defaultMaxWritebackBatchBytes)+1), "", "", "")
	add("/repo/huge/tail.txt", "tail", "", "", "")
	return records
}

var sizingCaps = []int64{0, 1, 300, 4 << 10, 64 << 10, 1 << 20, defaultMaxWritebackBatchBytes}

func TestBulkWriteRequestSizerMatchesMarshal(t *testing.T) {
	records := mixedSizeOutboxCorpus(1)
	var sizer bulkWriteRequestSizer
	for i, record := range records {
		size, ok := bulkWriteFileEncodedSize(outboxRecordAsBulkFile(record))
		sizer.add(size, ok)
		if i%37 != 0 && i != len(records)-1 {
			continue
		}
		want := legacyBulkWriteRequestSize(outboxRecordsAsBulkFiles(records[:i+1]))
		if got := sizer.size(); got != want {
			t.Fatalf("after %d records: incremental size %d, marshal size %d", i+1, got, want)
		}
	}
}

func TestChunkOutboxRecordsMatchesLegacyBoundaries(t *testing.T) {
	for _, seed := range []int64{1, 2} {
		records := mixedSizeOutboxCorpus(seed)
		for _, maxBytes := range sizingCaps {
			got := chunkOutboxRecords(records, maxBytes)
			want := legacyChunkOutboxRecords(records, maxBytes)
			if len(got) != len(want) {
				t.Fatalf("seed=%d max=%d: %d chunks, legacy %d", seed, maxBytes, len(got), len(want))
			}
			for i := range want {
				if len(got[i]) != len(want[i]) {
					t.Fatalf("seed=%d max=%d chunk %d: %d files, legacy %d", seed, maxBytes, i, len(got[i]), len(want[i]))
				}
				for j := range want[i] {
					if got[i][j].CommandID != want[i][j].CommandID {
						t.Fatalf("seed=%d max=%d chunk %d[%d]: %s, legacy %s", seed, maxBytes, i, j, got[i][j].CommandID, want[i][j].CommandID)
					}
				}
				gotBytes := legacyBulkWriteRequestSize(outboxRecordsAsBulkFiles(got[i]))
				wantBytes := legacyBulkWriteRequestSize(outboxRecordsAsBulkFiles(want[i]))
				if gotBytes != wantBytes {
					t.Fatalf("seed=%d max=%d chunk %d: %d request bytes, legacy %d", seed, maxBytes, i, gotBytes, wantBytes)
				}
			}
		}
	}
}

func TestBulkWriteChunkBoundsMatchesLegacyBoundaries(t *testing.T) {
	for _, seed := range []int64{1, 2} {
		files := outboxRecordsAsBulkFiles(mixedSizeOutboxCorpus(seed))
		for _, maxBytes := range sizingCaps {
			got := bulkWriteChunkBounds(files, maxBytes)
			want := legacyWriteFilesBulkBounds(files, maxBytes)
			if len(got) != len(want) {
				t.Fatalf("seed=%d max=%d: %d requests, legacy %d", seed, maxBytes, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("seed=%d max=%d request %d: %+v, legacy %+v", seed, maxBytes, i, got[i], want[i])
				}
			}
		}
	}
}

func TestChunkPendingBulkWritesMatchesLegacyBoundaries(t *testing.T) {
	records := mixedSizeOutboxCorpus(4)
	pending := make([]pendingBulkWrite, 0, len(records))
	for _, record := range records {
		pending = append(pending, pendingBulkWrite{
			remotePath: record.RemotePath,
			snapshot: localSnapshot{
				WireContent: record.Content,
				ContentType: record.ContentType,
				Encoding:    record.Encoding,
				Type:        record.Type,
				Target:      record.Target,
				Mode:        record.Mode,
				Hash:        record.Hash,
			},
		})
	}
	for _, maxBytes := range sizingCaps {
		got := chunkPendingBulkWrites("ws_sizing", pending, maxBytes)
		want := legacyChunkPendingBulkWrites("ws_sizing", pending, maxBytes)
		if len(got) != len(want) {
			t.Fatalf("max=%d: %d chunks, legacy %d", maxBytes, len(got), len(want))
		}
		for i := range want {
			if len(got[i]) != len(want[i]) || got[i][0].remotePath != want[i][0].remotePath {
				t.Fatalf("max=%d chunk %d differs: %d files from %s, legacy %d from %s", maxBytes, i, len(got[i]), got[i][0].remotePath, len(want[i]), want[i][0].remotePath)
			}
		}
	}
}
