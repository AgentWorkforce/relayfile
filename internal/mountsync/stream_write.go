package mountsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// WriteFilesBulk bounds JSON independently of the decoded per-file limit.
// Large singletons use the advertised raw streaming contract. Legacy Go
// servers retain their existing bounded 96 MiB JSON path.
func (c *HTTPClient) WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
	if len(files) == 0 {
		return BulkWriteResponse{}, ErrEmptyBulkWrite
	}
	var aggregate BulkWriteResponse
	for _, bounds := range bulkWriteChunkBounds(files, defaultMaxWritebackBatchBytes) {
		chunk := files[bounds.start:bounds.end]
		var out BulkWriteResponse
		var err error
		if len(chunk) == 1 && bounds.size > defaultMaxWritebackBatchBytes {
			out, err = c.writeLargeFile(ctx, workspaceID, chunk[0])
		} else {
			out, err = c.writeFilesBulkJSON(ctx, workspaceID, chunk)
		}
		if err != nil {
			return aggregate, err
		}
		aggregate.Written += out.Written
		aggregate.ErrorCount += out.ErrorCount
		aggregate.Errors = append(aggregate.Errors, out.Errors...)
		aggregate.Results = append(aggregate.Results, out.Results...)
		aggregate.CorrelationID = out.CorrelationID
	}
	return aggregate, nil
}

type bulkWriteChunkBound struct {
	start, end int
	// size is bulkWriteRequestSize(files[start:end]).
	size int64
}

// bulkWriteChunkBounds greedily packs files into JSON requests of at most
// maxBytes, always taking at least one file per request. Each file is encoded
// once; the boundaries match re-measuring bulkWriteRequestSize on every
// append (TestBulkWriteChunkBoundsMatchesLegacyBoundaries).
func bulkWriteChunkBounds(files []BulkWriteFile, maxBytes int64) []bulkWriteChunkBound {
	type encoded struct {
		size int64
		ok   bool
	}
	sizes := make([]encoded, len(files))
	for i := range files {
		sizes[i].size, sizes[i].ok = bulkWriteFileEncodedSize(files[i])
	}
	bounds := make([]bulkWriteChunkBound, 0, 1)
	for start := 0; start < len(files); {
		var sizer bulkWriteRequestSizer
		sizer.add(sizes[start].size, sizes[start].ok)
		end := start + 1
		for end < len(files) && sizer.sizeWith(sizes[end].size, sizes[end].ok) <= maxBytes {
			sizer.add(sizes[end].size, sizes[end].ok)
			end++
		}
		bounds = append(bounds, bulkWriteChunkBound{start: start, end: end, size: sizer.size()})
		start = end
	}
	return bounds
}

func (c *HTTPClient) writeLargeFile(ctx context.Context, workspaceID string, file BulkWriteFile) (BulkWriteResponse, error) {
	if isSymlinkType(file.Type) || file.WritebackIntent != "" {
		return c.writeFilesBulkJSON(ctx, workspaceID, []BulkWriteFile{file})
	}

	var health struct {
		Features []string `json:"features"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/health", nil, nil, &health); err != nil {
		if ctx.Err() != nil {
			return BulkWriteResponse{}, ctx.Err()
		}
		// Discovery is optional on older servers/proxies. Failure must not
		// remove their existing bounded JSON write capability.
		return c.writeFilesBulkJSON(ctx, workspaceID, []BulkWriteFile{file})
	}
	streamSupported := false
	for _, feature := range health.Features {
		if feature == "file-stream-v1" {
			streamSupported = true
		}
	}
	if !streamSupported {
		return c.writeFilesBulkJSON(ctx, workspaceID, []BulkWriteFile{file})
	}
	var raw []byte
	var err error
	if normalizeEncoding(file.Encoding) == "base64" {
		raw, err = base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(file.Content)
		}
	} else {
		raw = []byte(file.Content)
	}
	if err != nil {
		return BulkWriteResponse{}, fmt.Errorf("invalid base64 for %s", file.Path)
	}
	if maxBytes := maxWritebackBytes(); maxBytes > 0 && int64(len(raw)) > maxBytes {
		return BulkWriteResponse{}, fmt.Errorf("file %s exceeds the configured %d-byte writeback limit", file.Path, maxBytes)
	}
	ifMatch := file.IfMatch
	if ifMatch == "" {
		ifMatch = "*"
	}
	mode := file.Mode
	if mode == 0 {
		mode = 0644
	}
	encoding := normalizeEncoding(file.Encoding)
	if encoding == "" {
		encoding = "utf-8"
	}
	headers := map[string]string{
		"Content-Type":             "application/octet-stream",
		"X-Relayfile-Encoding":     encoding,
		"X-Relayfile-Content-Type": file.ContentType,
		"X-Relayfile-Mode":         fmt.Sprintf("%03o", mode&0777),
		"If-Match":                 ifMatch,
	}
	if file.ContentIdentity != nil {
		identity, err := json.Marshal(file.ContentIdentity)
		if err != nil {
			return BulkWriteResponse{}, err
		}
		headers["X-Relayfile-Content-Identity"] = string(identity)
	}
	q := url.Values{"path": []string{normalizeRemotePath(file.Path)}}
	var result WriteResult
	err = c.doBytesWithLimit(ctx, http.MethodPut, fmt.Sprintf("/v1/workspaces/%s/fs/file?%s", url.PathEscape(workspaceID), q.Encode()), headers, raw, &result, 1<<20, false)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return BulkWriteResponse{ErrorCount: 1, Errors: []BulkWriteError{{Path: file.Path, Code: "revision_conflict", Message: "revision conflict"}}}, nil
		}
		return BulkWriteResponse{}, err
	}
	return BulkWriteResponse{Written: 1, Results: []BulkWriteResult{{Path: file.Path, Revision: result.TargetRevision, OpID: result.OpID, ContentType: file.ContentType}}}, nil
}
