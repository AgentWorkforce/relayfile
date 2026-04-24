package mountsync

import (
	"errors"
	"strings"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

type BulkWriteFile = relayfile.BulkWriteFile

type BulkWriteError = relayfile.BulkWriteError

type BulkWriteResult struct {
	Path        string `json:"path"`
	Revision    string `json:"revision"`
	ContentType string `json:"contentType,omitempty"`
}

type BulkWriteResponse struct {
	Written       int               `json:"written"`
	ErrorCount    int               `json:"errorCount"`
	Errors        []BulkWriteError  `json:"errors"`
	Results       []BulkWriteResult `json:"results,omitempty"`
	Files         []RemoteFile      `json:"files,omitempty"`
	CorrelationID string            `json:"correlationId"`
}

var ErrEmptyBulkWrite = errors.New("bulk write requires at least one file")

func (r BulkWriteResponse) revisionsByPath() map[string]string {
	byPath := make(map[string]string, len(r.Results)+len(r.Files))
	for _, result := range r.Results {
		revision := strings.TrimSpace(result.Revision)
		if revision == "" {
			continue
		}
		byPath[normalizeRemotePath(result.Path)] = revision
	}
	for _, file := range r.Files {
		revision := strings.TrimSpace(file.Revision)
		if revision == "" {
			continue
		}
		byPath[normalizeRemotePath(file.Path)] = revision
	}
	return byPath
}
