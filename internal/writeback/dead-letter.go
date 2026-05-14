// Package writeback owns the dead-letter sidecar format produced when a
// queued writeback can never be completed. Each failed payload is parked
// alongside an `<file>.error.json` describing the cause, matching the
// schema at schemas/relay/dead-letter-error.schema.json.
package writeback

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SchemaPath is the canonical location of the dead-letter sidecar schema
// inside the schemas embed FS.
const SchemaPath = "relay/dead-letter-error.schema.json"

// ErrorCode is the enum of `code` values valid in the sidecar schema.
type ErrorCode string

const (
	CodeSchemaViolation     ErrorCode = "schema_violation"
	CodeProvider4xx         ErrorCode = "provider_4xx"
	CodeProvider5xxExhaust  ErrorCode = "provider_5xx_exhausted"
	CodeTimeout             ErrorCode = "timeout"
)

// ErrorContext is the structured payload written to `<file>.error.json`.
// Field tags match the JSON Schema; integers/strings stay primitive so the
// santhosh-tekuri/jsonschema validator can check them directly.
type ErrorContext struct {
	Code             ErrorCode   `json:"code"`
	Message          string      `json:"message"`
	ProviderStatus   int         `json:"providerStatus,omitempty"`
	ProviderResponse interface{} `json:"providerResponse,omitempty"`
	Attempts         int         `json:"attempts"`
	FirstAttemptAt   time.Time   `json:"firstAttemptAt"`
	LastAttemptAt    time.Time   `json:"lastAttemptAt"`
	OpID             string      `json:"opId"`
}

// DeadEntry is the merged view of a dead-letter file and its sidecar, used
// by the CLI `writeback list --state dead` surface.
type DeadEntry struct {
	PayloadPath string          `json:"payloadPath"`
	SidecarPath string          `json:"sidecarPath"`
	Payload     json.RawMessage `json:"payload"`
	Error       ErrorContext    `json:"error"`
}

// WriteDeadLetter persists `original` at `payloadPath` and the sidecar
// JSON at `<payloadPath>.error.json`. Both writes are atomic (temp file +
// rename) and the parent directory is created if missing.
func WriteDeadLetter(payloadPath string, original []byte, errCtx ErrorContext) error {
	if payloadPath == "" {
		return errors.New("writeback: empty payloadPath")
	}
	if errCtx.Code == "" {
		return errors.New("writeback: ErrorContext.Code is required")
	}
	if strings.TrimSpace(errCtx.Message) == "" {
		return errors.New("writeback: ErrorContext.Message is required")
	}
	if errCtx.Attempts < 1 {
		return errors.New("writeback: ErrorContext.Attempts must be >= 1")
	}
	if errCtx.OpID == "" {
		return errors.New("writeback: ErrorContext.OpID is required")
	}

	dir := filepath.Dir(payloadPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := atomicWrite(payloadPath, original); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}

	sidecar, err := MarshalSidecar(errCtx)
	if err != nil {
		return err
	}
	sidecarPath := payloadPath + ".error.json"
	if err := atomicWrite(sidecarPath, sidecar); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}
	return nil
}

// MarshalSidecar serializes the ErrorContext using the wire format the
// schema expects (RFC3339 timestamps, fixed key order via encoding/json).
func MarshalSidecar(errCtx ErrorContext) ([]byte, error) {
	type wire struct {
		Code             ErrorCode   `json:"code"`
		Message          string      `json:"message"`
		ProviderStatus   int         `json:"providerStatus,omitempty"`
		ProviderResponse interface{} `json:"providerResponse,omitempty"`
		Attempts         int         `json:"attempts"`
		FirstAttemptAt   string      `json:"firstAttemptAt"`
		LastAttemptAt    string      `json:"lastAttemptAt"`
		OpID             string      `json:"opId"`
	}
	out := wire{
		Code:             errCtx.Code,
		Message:          errCtx.Message,
		ProviderStatus:   errCtx.ProviderStatus,
		ProviderResponse: errCtx.ProviderResponse,
		Attempts:         errCtx.Attempts,
		FirstAttemptAt:   errCtx.FirstAttemptAt.UTC().Format(time.RFC3339),
		LastAttemptAt:    errCtx.LastAttemptAt.UTC().Format(time.RFC3339),
		OpID:             errCtx.OpID,
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar: %w", err)
	}
	return append(buf, '\n'), nil
}

// ListDead walks `<root>` (typically `<workspace>/.relay/dead-letter`) and
// returns merged payload + sidecar records. Files without a paired
// sidecar are skipped silently — they represent in-flight writes whose
// rename ordering has not completed.
func ListDead(root string) ([]DeadEntry, error) {
	info, err := os.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("dead-letter root is not a directory: %s", root)
	}

	var entries []DeadEntry
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".error.json") {
			return nil
		}
		sidecarPath := path + ".error.json"
		sidecarBytes, err := os.ReadFile(sidecarPath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read sidecar %s: %w", sidecarPath, err)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read payload %s: %w", path, err)
		}
		var ctx ErrorContext
		if err := json.Unmarshal(sidecarBytes, &ctx); err != nil {
			return fmt.Errorf("parse sidecar %s: %w", sidecarPath, err)
		}
		entries = append(entries, DeadEntry{
			PayloadPath: path,
			SidecarPath: sidecarPath,
			Payload:     payload,
			Error:       ctx,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].PayloadPath < entries[j].PayloadPath
	})
	return entries, nil
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dlq-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
