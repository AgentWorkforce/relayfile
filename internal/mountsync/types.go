package mountsync

import (
	"errors"
	"strings"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

type BulkWriteFile = relayfile.BulkWriteFile

type BulkWriteError = relayfile.BulkWriteError

type BulkWriteResult = relayfile.BulkWriteResult

type BulkWriteResponse struct {
	Written       int               `json:"written"`
	ErrorCount    int               `json:"errorCount"`
	Errors        []BulkWriteError  `json:"errors"`
	Results       []BulkWriteResult `json:"results,omitempty"`
	CorrelationID string            `json:"correlationId"`
}

var ErrEmptyBulkWrite = errors.New("bulk write requires at least one file")

func (r BulkWriteResponse) resultsByPath() map[string]BulkWriteResult {
	byPath := make(map[string]BulkWriteResult, len(r.Results))
	for _, result := range r.Results {
		normalizedPath := normalizeRemotePath(result.Path)
		result.Path = normalizedPath
		result.Revision = strings.TrimSpace(result.Revision)
		result.ContentType = strings.TrimSpace(result.ContentType)
		byPath[normalizedPath] = result
	}
	return byPath
}
