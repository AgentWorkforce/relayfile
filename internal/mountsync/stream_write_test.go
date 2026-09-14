package mountsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientBulkWriteSplitsProductionSizedSeed(t *testing.T) {
	files := make([]BulkWriteFile, 67)
	for i := range files {
		files[i] = BulkWriteFile{Path: fmt.Sprintf("/.repo/file%d", i), Content: strings.Repeat("x", 1<<20), Mode: 0755}
	}
	seen := make(map[string]bool)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.ContentLength > 10551296 {
			t.Errorf("oversized production request: %d", r.ContentLength)
			w.WriteHeader(413)
			return
		}
		var body struct {
			Files []BulkWriteFile `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var results []BulkWriteResult
		for _, file := range body.Files {
			if seen[file.Path] || file.Mode != 0755 || len(file.Content) != 1<<20 {
				t.Errorf("changed or duplicated file %s", file.Path)
			}
			seen[file.Path] = true
			results = append(results, BulkWriteResult{Path: file.Path, Revision: "rev_1"})
		}
		json.NewEncoder(w).Encode(BulkWriteResponse{Written: len(body.Files), Results: results})
	}))
	defer server.Close()
	out, err := NewHTTPClient(server.URL, "local-test", server.Client()).WriteFilesBulk(context.Background(), "workspace", files)
	if err != nil || out.Written != len(files) || len(seen) != len(files) || calls < 2 {
		t.Fatalf("seed: written=%d seen=%d calls=%d err=%v", out.Written, len(seen), calls, err)
	}
}

func TestHTTPClientStreams64MiBBinaryWithMetadata(t *testing.T) {
	raw := bytes.Repeat([]byte{0, 255, 128, 42}, 16<<20)
	wantHash := sha256.Sum256(raw)
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			io.WriteString(w, `{"features":["file-stream-v1"]}`)
			return
		}
		puts++
		if r.Method != "PUT" || !strings.HasSuffix(r.URL.Path, "/fs/file") || r.URL.Query().Get("path") != "/bin/tool" {
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
		}
		if r.ContentLength != 64<<20 || r.Header.Get("Content-Type") != "application/octet-stream" || r.Header.Get("X-Relayfile-Mode") != "755" || r.Header.Get("If-Match") != "rev_10" {
			t.Error("lost streaming metadata")
		}
		var identity ContentIdentity
		if json.Unmarshal([]byte(r.Header.Get("X-Relayfile-Content-Identity")), &identity) != nil || identity.Key != "stable-command" {
			t.Error("lost durable identity")
		}
		h := sha256.New()
		n, err := io.Copy(h, r.Body)
		if err != nil || n != 64<<20 || !bytes.Equal(h.Sum(nil), wantHash[:]) {
			t.Error("binary changed")
		}
		io.WriteString(w, `{"targetRevision":"rev_11","opId":"op_11"}`)
	}))
	defer server.Close()
	file := BulkWriteFile{Path: "/bin/tool", Content: base64.StdEncoding.EncodeToString(raw), Encoding: "base64", ContentType: "application/octet-stream", Mode: 0755, IfMatch: "rev_10", ContentIdentity: &ContentIdentity{Kind: "mount-command", Key: "stable-command"}}
	out, err := NewHTTPClient(server.URL, "local-test", server.Client()).WriteFilesBulk(context.Background(), "workspace", []BulkWriteFile{file})
	if err != nil || puts != 1 || len(out.Results) != 1 || out.Results[0].Revision != "rev_11" {
		t.Fatalf("stream result %#v err=%v", out, err)
	}
}

func TestHTTPClientStreamingConflictRemainsPerFileError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			io.WriteString(w, `{"features":["file-stream-v1"]}`)
			return
		}
		w.WriteHeader(409)
		io.WriteString(w, `{"code":"revision_conflict","message":"changed"}`)
	}))
	defer server.Close()
	out, err := NewHTTPClient(server.URL, "local-test", server.Client()).WriteFilesBulk(context.Background(), "workspace", []BulkWriteFile{{Path: "/large", Content: strings.Repeat("x", 9<<20), IfMatch: "rev_1"}})
	if err != nil || out.ErrorCount != 1 || out.Errors[0].Code != "revision_conflict" {
		t.Fatalf("lost conflict: %#v %v", out, err)
	}
}

func TestHTTPClientLargeUTF8AndConfiguredLimits(t *testing.T) {
	content := strings.Repeat("x", 65<<20)
	for _, limit := range []string{"0", "68157440"} {
		t.Run(limit, func(t *testing.T) {
			t.Setenv("RELAYFILE_MAX_WRITEBACK_BYTES", limit)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					io.WriteString(w, `{"features":["file-stream-v1"]}`)
					return
				}
				if r.Header.Get("X-Relayfile-Encoding") != "utf-8" {
					t.Error("missing explicit UTF-8 representation")
				}
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil || n != int64(len(content)) {
					t.Errorf("content size=%d err=%v", n, err)
				}
				io.WriteString(w, `{"targetRevision":"rev_1"}`)
			}))
			defer server.Close()
			out, err := NewHTTPClient(server.URL, "local-test", server.Client()).WriteFilesBulk(context.Background(), "workspace", []BulkWriteFile{{Path: "/large.txt", Content: content}})
			if err != nil || out.Written != 1 {
				t.Fatalf("configured limit rejected: %#v %v", out, err)
			}
		})
	}
}

func TestHTTPClientLargeMetadataWriteKeepsJSONContract(t *testing.T) {
	file := BulkWriteFile{Path: "/provider/draft", Content: strings.Repeat("x", 9<<20), WritebackIntent: "provider-specific"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/fs/bulk") {
			t.Errorf("metadata write lost JSON route %s", r.URL.Path)
		}
		var body struct {
			Files []BulkWriteFile `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Files) != 1 || body.Files[0].WritebackIntent != file.WritebackIntent {
			t.Error("metadata lost")
		}
		io.WriteString(w, `{"written":1}`)
	}))
	defer server.Close()
	out, err := NewHTTPClient(server.URL, "local-test", server.Client()).WriteFilesBulk(context.Background(), "workspace", []BulkWriteFile{file})
	if err != nil || out.Written != 1 {
		t.Fatalf("metadata result %#v %v", out, err)
	}
}

func TestHTTPClientLargeWriteRetainsJSONWhenDiscoveryFails(t *testing.T) {
	for _, status := range []int{200, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(status)
					io.WriteString(w, "legacy health response")
					return
				}
				if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/fs/bulk") {
					t.Errorf("unexpected fallback route %s", r.URL.Path)
				}
				if r.ContentLength > 96<<20 {
					t.Error("unbounded JSON fallback")
				}
				io.Copy(io.Discard, r.Body)
				writes++
				io.WriteString(w, `{"written":1}`)
			}))
			defer server.Close()
			client := NewHTTPClient(server.URL, "local-test", server.Client())
			client.maxRetries = 0
			out, err := client.WriteFilesBulk(context.Background(), "workspace", []BulkWriteFile{{Path: "/large.txt", Content: strings.Repeat("x", 9<<20)}})
			if err != nil || writes != 1 || out.Written != 1 {
				t.Fatalf("fallback failed: %#v %v writes=%d", out, err, writes)
			}
		})
	}
}

func TestRemoteBase64MaterializationAt64MiBBoundary(t *testing.T) {
	raw := bytes.Repeat([]byte{0xa5}, (64<<20)+1)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		file := RemoteFile{Encoding: "base64", Content: encoding.EncodeToString(raw[:64<<20])}
		decoded, err := decodeRemoteFileContent(file)
		if err != nil || !bytes.Equal(decoded, raw[:64<<20]) {
			t.Fatalf("exact64MiB rejected/corrupted: %v", err)
		}
		file.Content = encoding.EncodeToString(raw)
		if _, err := decodeRemoteFileContent(file); err == nil {
			t.Fatal("accepted above64MiB")
		}
	}
}

func TestRemoteBase64LimitCountsBytesNotLineBreaks(t *testing.T) {
	t.Setenv("RELAYFILE_MAX_WRITEBACK_BYTES", "1")
	decoded, err := decodeRemoteFileContent(RemoteFile{Encoding: "base64", Content: "YQ==\r\n"})
	if err != nil || string(decoded) != "a" {
		t.Fatalf("padded one-byte content failed: %v", err)
	}
	if _, err := decodeRemoteFileContent(RemoteFile{Encoding: "base64", Content: "Y@=="}); err == nil {
		t.Fatal("accepted invalid base64")
	}
}
