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
	for start := 0; start < len(files); {
		end := start + 1
		for end < len(files) && bulkWriteRequestSize(files[start:end+1]) <= defaultMaxWritebackBatchBytes {
			end++
		}
		chunk := files[start:end]
		var out BulkWriteResponse
		var err error
		if len(chunk) == 1 && bulkWriteRequestSize(chunk) > defaultMaxWritebackBatchBytes {
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
		start = end
	}
	return aggregate, nil
}

func (c *HTTPClient) writeLargeFile(ctx context.Context, workspaceID string, file BulkWriteFile) (BulkWriteResponse, error) {
	var health struct {
		Features []string `json:"features"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/health", nil, nil, &health); err != nil {
		return BulkWriteResponse{}, err
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
	if isSymlinkType(file.Type) || file.WritebackIntent != "" {
		return BulkWriteResponse{}, fmt.Errorf("large streaming write unsupported for metadata on %s", file.Path)
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
	if len(raw) > 64<<20 {
		return BulkWriteResponse{}, fmt.Errorf("file %s exceeds the 64 MiB streaming limit", file.Path)
	}
	ifMatch := file.IfMatch
	if ifMatch == "" {
		ifMatch = "*"
	}
	mode := file.Mode
	if mode == 0 {
		mode = 0644
	}
	headers := map[string]string{
		"Content-Type":             "application/octet-stream",
		"X-Relayfile-Encoding":     normalizeEncoding(file.Encoding),
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
