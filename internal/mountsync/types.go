package mountsync

import (
	"errors"
	"strings"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

type BulkWriteFile = relayfile.BulkWriteFile

type ContentIdentity = relayfile.ContentIdentity

type BulkWriteError = relayfile.BulkWriteError

type BulkWriteResult = relayfile.BulkWriteResult

type OperationStatus = relayfile.OperationStatus

type CheckpointSeal = relayfile.CheckpointSeal

type CheckpointSealRequest = relayfile.CheckpointSealRequest

type CheckpointSealConsumeRequest = relayfile.CheckpointSealConsumeRequest

type CheckpointSealConsumeRecoveryRequest = relayfile.CheckpointSealConsumeRecoveryRequest

type CheckpointSealVerifyRequest = relayfile.CheckpointSealVerifyRequest

type CheckpointSealHandbackRequest = relayfile.CheckpointSealHandbackRequest

type CheckpointSealResumeRequest = relayfile.CheckpointSealResumeRequest

type CheckpointSealOwnership = relayfile.CheckpointSealOwnership

const DefaultCheckpointSealTTL = relayfile.DefaultCheckpointSealTTL

const MaxCheckpointSealTTL = relayfile.MaxCheckpointSealTTL

const CheckpointHandbackPhasePrepare = relayfile.CheckpointHandbackPhasePrepare

const CheckpointHandbackPhaseCommit = relayfile.CheckpointHandbackPhaseCommit

type BulkWriteResponse struct {
	Written       int               `json:"written"`
	ErrorCount    int               `json:"errorCount"`
	Errors        []BulkWriteError  `json:"errors"`
	Results       []BulkWriteResult `json:"results,omitempty"`
	CorrelationID string            `json:"correlationId"`
}

var ErrEmptyBulkWrite = errors.New("bulk write requires at least one file")

type ProviderLayoutManifest struct {
	Provider      string
	Resources     []string
	AliasSegments []string
}

type ProviderLayoutRegistrar interface {
	RegisterProviderLayout(provider string, manifest ProviderLayoutManifest) error
}

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
