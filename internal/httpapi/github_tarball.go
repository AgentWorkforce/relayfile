package httpapi

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

// GitHub tarball import — server-side companion to the cloud
// github-clone-tar-importer client. Three endpoints:
//
//	POST /v1/workspaces/{id}/fs/import/github-tarball          (direct gzip body)
//	POST /v1/workspaces/{id}/fs/import/github-tarball/fetch    (server-side fetch job)
//	GET  /v1/workspaces/{id}/fs/import/github-tarball/jobs/{jobId}
//
// Extracted entries are written into the workspace via the existing store
// bulk-write machinery so every imported file emits a normal filesystem
// event (digest regeneration consumes those events; see
// .claude/rules/relayfile-integration-digests.md).

const (
	githubTarballMaxFileBytes    = 1 << 20 // per-file cap, mirrors cloud walker
	githubTarballBinarySniffSize = 8 << 10
	githubTarballBulkChunkSize   = 200
	defaultMaxGithubTarballBytes = int64(1) << 30
	githubTarballJobRetention    = time.Hour
	githubTarballFetchTimeout    = 15 * time.Minute
)

var githubTarballIgnoreDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, ".next": {}, ".open-next": {},
	"dist": {}, "build": {}, "coverage": {}, ".turbo": {}, ".yarn": {},
}

var githubTarballIgnoreFiles = map[string]struct{}{
	"package-lock.json": {}, "yarn.lock": {}, "pnpm-lock.yaml": {}, "bun.lockb": {},
}

var githubTarballIgnoreExts = []string{".min.js", ".min.css", ".map"}

var githubTarballBinaryExts = []string{
	".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip",
	".tar", ".gz", ".woff", ".woff2", ".ttf", ".otf", ".eot", ".mp4",
	".mov", ".wasm",
}

type githubTarImportSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// githubTarImportJob is the pollable state for an async fetch+import job.
// Status values match the cloud importer contract:
// queued | fetching | importing | completed | failed.
type githubTarImportJob struct {
	JobID        string                     `json:"jobId"`
	WorkspaceID  string                     `json:"workspaceId"`
	Owner        string                     `json:"owner"`
	Repo         string                     `json:"repo"`
	Ref          string                     `json:"ref,omitempty"`
	HeadSha      string                     `json:"headSha"`
	Status       string                     `json:"status"`
	Imported     int                        `json:"imported"`
	ErrorCount   int                        `json:"errorCount"`
	Errors       []relayfile.BulkWriteError `json:"errors"`
	Skipped      []githubTarImportSkip      `json:"skipped"`
	BytesWritten int64                      `json:"bytesWritten"`
	LastError    string                     `json:"lastError,omitempty"`
	CreatedAt    string                     `json:"createdAt"`
	UpdatedAt    string                     `json:"updatedAt"`
	CompletedAt  string                     `json:"completedAt,omitempty"`

	tarballURL string
}

type githubTarImportSummary struct {
	Imported     int
	Errors       []relayfile.BulkWriteError
	Skipped      []githubTarImportSkip
	BytesWritten int64
}

type githubTarballEntry struct {
	repoPath string
	file     relayfile.BulkWriteFile
	size     int64
}

type githubTarballExtraction struct {
	entries []githubTarballEntry
	skipped []githubTarImportSkip
}

// validGithubSlugComponent rejects owner/repo values that could distort the
// destination workspace path (separators, traversal segments, whitespace).
func validGithubSlugComponent(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, "/\\ \t\r\n")
}

func newGithubTarballJobID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func (s *Server) maxGithubTarballBytes() int64 {
	if s.cfg.MaxGithubTarballBytes > 0 {
		return s.cfg.MaxGithubTarballBytes
	}
	return defaultMaxGithubTarballBytes
}

// ── handlers ────────────────────────────────────────────────────────────────

func (s *Server) handleGithubTarballImport(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string, claims tokenClaims) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	headSha := strings.TrimSpace(r.URL.Query().Get("headSha"))
	jobID := strings.TrimSpace(r.URL.Query().Get("jobId"))
	if owner == "" || repo == "" || headSha == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing owner, repo, or headSha query parameter", correlationID)
		return
	}
	if !validGithubSlugComponent(owner) || !validGithubSlugComponent(repo) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid owner or repo", correlationID)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxGithubTarballBytes())
	extraction, err := extractGithubTarball(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "tarball exceeds configured limit", correlationID)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_tarball", "failed to extract gzip tarball: "+err.Error(), correlationID)
		return
	}

	summary := s.importGithubTarballEntries(workspaceID, owner, repo, headSha, "", jobID, correlationID, extraction, claims)
	if jobID != "" {
		s.recordCompletedGithubTarballJob(workspaceID, owner, repo, "", headSha, jobID, summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"imported":      summary.Imported,
		"errorCount":    len(summary.Errors),
		"errors":        summary.Errors,
		"skipped":       summary.Skipped,
		"bytesWritten":  summary.BytesWritten,
		"correlationId": correlationID,
	})
}

func (s *Server) handleGithubTarballFetch(w http.ResponseWriter, r *http.Request, workspaceID, correlationID string, claims tokenClaims) {
	var body struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		Ref        string `json:"ref"`
		HeadSha    string `json:"headSha"`
		JobID      string `json:"jobId"`
		TarballURL string `json:"tarballUrl"`
	}
	if !s.decodeJSONBody(w, r, correlationID, &body) {
		return
	}
	owner := strings.TrimSpace(body.Owner)
	repo := strings.TrimSpace(body.Repo)
	headSha := strings.TrimSpace(body.HeadSha)
	tarballURL := strings.TrimSpace(body.TarballURL)
	if owner == "" || repo == "" || headSha == "" || tarballURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing owner, repo, headSha, or tarballUrl", correlationID)
		return
	}
	if !validGithubSlugComponent(owner) || !validGithubSlugComponent(repo) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid owner or repo", correlationID)
		return
	}
	parsed, err := url.Parse(tarballURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "tarballUrl must be an absolute http(s) URL", correlationID)
		return
	}
	jobID := strings.TrimSpace(body.JobID)
	if jobID == "" {
		jobID = newGithubTarballJobID()
	}
	githubToken := strings.TrimSpace(r.Header.Get("X-GitHub-Token"))

	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.githubTarJobsMu.Lock()
	s.pruneGithubTarballJobsLocked(time.Now().UTC())
	if existing, ok := s.githubTarJobs[githubTarballJobKey(workspaceID, jobID)]; ok {
		snapshot := existing.snapshot()
		s.githubTarJobsMu.Unlock()
		writeJSON(w, http.StatusAccepted, snapshot)
		return
	}
	if active := s.activeGithubTarballJobLocked(workspaceID, owner, repo, headSha); active != nil {
		snapshot := active.snapshot()
		s.githubTarJobsMu.Unlock()
		writeJSON(w, http.StatusAccepted, snapshot)
		return
	}
	job := &githubTarImportJob{
		JobID:       jobID,
		WorkspaceID: workspaceID,
		Owner:       owner,
		Repo:        repo,
		Ref:         strings.TrimSpace(body.Ref),
		HeadSha:     headSha,
		Status:      "queued",
		Errors:      []relayfile.BulkWriteError{},
		Skipped:     []githubTarImportSkip{},
		CreatedAt:   now,
		UpdatedAt:   now,
		tarballURL:  tarballURL,
	}
	s.githubTarJobs[githubTarballJobKey(workspaceID, jobID)] = job
	snapshot := job.snapshot()
	s.githubTarJobsMu.Unlock()

	go s.runGithubTarballFetchJob(job, githubToken, correlationID, claims)

	writeJSON(w, http.StatusAccepted, snapshot)
}

func (s *Server) handleGithubTarballJob(w http.ResponseWriter, _ *http.Request, workspaceID, jobID, correlationID string) {
	s.githubTarJobsMu.Lock()
	job, ok := s.githubTarJobs[githubTarballJobKey(workspaceID, jobID)]
	var snapshot githubTarImportJob
	if ok {
		snapshot = job.snapshot()
	}
	s.githubTarJobsMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "github tarball import job not found", correlationID)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// ── job lifecycle ───────────────────────────────────────────────────────────

func githubTarballJobKey(workspaceID, jobID string) string {
	return workspaceID + "|" + jobID
}

func (j *githubTarImportJob) snapshot() githubTarImportJob {
	copied := *j
	copied.Errors = append([]relayfile.BulkWriteError(nil), j.Errors...)
	copied.Skipped = append([]githubTarImportSkip(nil), j.Skipped...)
	return copied
}

func (s *Server) activeGithubTarballJobLocked(workspaceID, owner, repo, headSha string) *githubTarImportJob {
	var active *githubTarImportJob
	for _, job := range s.githubTarJobs {
		if job.WorkspaceID != workspaceID || job.Owner != owner || job.Repo != repo || job.HeadSha != headSha {
			continue
		}
		switch job.Status {
		case "queued", "fetching", "importing":
			if active == nil || job.CreatedAt < active.CreatedAt {
				active = job
			}
		}
	}
	return active
}

func (s *Server) pruneGithubTarballJobsLocked(now time.Time) {
	for key, job := range s.githubTarJobs {
		if job.CompletedAt == "" {
			continue
		}
		completedAt, err := time.Parse(time.RFC3339Nano, job.CompletedAt)
		if err != nil {
			continue
		}
		if now.Sub(completedAt) > githubTarballJobRetention {
			delete(s.githubTarJobs, key)
		}
	}
}

func (s *Server) setGithubTarballJobStatus(job *githubTarImportJob, status string) {
	s.githubTarJobsMu.Lock()
	job.Status = status
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.githubTarJobsMu.Unlock()
}

func (s *Server) completeGithubTarballJob(job *githubTarImportJob, summary githubTarImportSummary) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.githubTarJobsMu.Lock()
	job.Status = "completed"
	job.Imported = summary.Imported
	job.ErrorCount = len(summary.Errors)
	job.Errors = summary.Errors
	job.Skipped = summary.Skipped
	job.BytesWritten = summary.BytesWritten
	job.UpdatedAt = now
	job.CompletedAt = now
	s.githubTarJobsMu.Unlock()
}

func (s *Server) failGithubTarballJob(job *githubTarImportJob, message string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.githubTarJobsMu.Lock()
	job.Status = "failed"
	job.LastError = message
	job.UpdatedAt = now
	job.CompletedAt = now
	s.githubTarJobsMu.Unlock()
}

func (s *Server) recordCompletedGithubTarballJob(workspaceID, owner, repo, ref, headSha, jobID string, summary githubTarImportSummary) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := &githubTarImportJob{
		JobID:        jobID,
		WorkspaceID:  workspaceID,
		Owner:        owner,
		Repo:         repo,
		Ref:          ref,
		HeadSha:      headSha,
		Status:       "completed",
		Imported:     summary.Imported,
		ErrorCount:   len(summary.Errors),
		Errors:       summary.Errors,
		Skipped:      summary.Skipped,
		BytesWritten: summary.BytesWritten,
		CreatedAt:    now,
		UpdatedAt:    now,
		CompletedAt:  now,
	}
	s.githubTarJobsMu.Lock()
	s.pruneGithubTarballJobsLocked(time.Now().UTC())
	s.githubTarJobs[githubTarballJobKey(workspaceID, jobID)] = job
	s.githubTarJobsMu.Unlock()
}

func (s *Server) githubTarballFetchClient() *http.Client {
	if s.githubTarballClient != nil {
		return s.githubTarballClient
	}
	return &http.Client{Timeout: githubTarballFetchTimeout}
}

func (s *Server) runGithubTarballFetchJob(job *githubTarImportJob, githubToken, correlationID string, claims tokenClaims) {
	s.setGithubTarballJobStatus(job, "fetching")

	req, err := http.NewRequest(http.MethodGet, job.tarballURL, nil)
	if err != nil {
		s.failGithubTarballJob(job, "invalid tarball url: "+err.Error())
		return
	}
	if githubToken != "" {
		req.Header.Set("Authorization", "token "+githubToken)
	}
	req.Header.Set("User-Agent", "relayfile-github-tarball-import")
	resp, err := s.githubTarballFetchClient().Do(req)
	if err != nil {
		s.failGithubTarballJob(job, "github tarball fetch failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		s.failGithubTarballJob(job, fmt.Sprintf("github tarball fetch failed: unexpected status %d", resp.StatusCode))
		return
	}

	s.setGithubTarballJobStatus(job, "importing")
	limited := io.LimitReader(resp.Body, s.maxGithubTarballBytes()+1)
	extraction, err := extractGithubTarball(limited)
	if err != nil {
		s.failGithubTarballJob(job, "failed to extract gzip tarball: "+err.Error())
		return
	}

	summary := s.importGithubTarballEntries(job.WorkspaceID, job.Owner, job.Repo, job.HeadSha, job.Ref, job.JobID, correlationID, extraction, claims)
	s.completeGithubTarballJob(job, summary)
}

// ── extraction ──────────────────────────────────────────────────────────────

// normalizeGithubTarballPath strips the tarball's single leading directory
// (GitHub archives wrap everything in `{owner}-{repo}-{sha}/`) and rejects
// unsafe entries. Returns "" when the entry should be silently dropped.
func normalizeGithubTarballPath(entryPath string) string {
	if entryPath == "" || strings.ContainsRune(entryPath, 0) {
		return ""
	}
	unixPath := strings.ReplaceAll(entryPath, "\\", "/")
	stripped := strings.TrimLeft(unixPath, "/")
	for strings.HasPrefix(stripped, "./") {
		stripped = strings.TrimPrefix(stripped, "./")
	}
	if stripped == "" {
		return ""
	}
	parts := make([]string, 0, 8)
	for _, part := range strings.Split(stripped, "/") {
		if part == "" {
			continue
		}
		if part == "." || part == ".." {
			return ""
		}
		parts = append(parts, part)
	}
	if len(parts) < 2 {
		return ""
	}
	repoPath := path.Clean(strings.Join(parts[1:], "/"))
	if repoPath == "" || repoPath == "." || strings.HasPrefix(repoPath, "../") {
		return ""
	}
	return repoPath
}

func githubTarballPathIgnored(repoPath string) bool {
	lowerPath := strings.ToLower(repoPath)
	parts := strings.Split(lowerPath, "/")
	for _, part := range parts {
		if _, ok := githubTarballIgnoreDirs[part]; ok {
			return true
		}
	}
	if _, ok := githubTarballIgnoreFiles[parts[len(parts)-1]]; ok {
		return true
	}
	for _, ext := range githubTarballIgnoreExts {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}
	return false
}

func githubTarballKnownBinary(repoPath string) bool {
	lowerPath := strings.ToLower(repoPath)
	for _, ext := range githubTarballBinaryExts {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}
	return false
}

func githubTarballLooksBinary(repoPath string, content []byte) bool {
	if githubTarballKnownBinary(repoPath) {
		return true
	}
	sniff := content
	if len(sniff) > githubTarballBinarySniffSize {
		sniff = sniff[:githubTarballBinarySniffSize]
	}
	for _, b := range sniff {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(content)
}

// extractGithubTarball walks a gzipped GitHub source tarball and converts
// regular file entries into bulk-write inputs. Paths in skip/error records
// are repo-relative, matching the cloud importer contract.
func extractGithubTarball(r io.Reader) (*githubTarballExtraction, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("invalid gzip stream: %w", err)
	}
	defer gzr.Close()

	extraction := &githubTarballExtraction{
		skipped: []githubTarImportSkip{},
	}
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid tar stream: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		repoPath := normalizeGithubTarballPath(header.Name)
		if repoPath == "" {
			continue
		}
		if githubTarballPathIgnored(repoPath) {
			extraction.skipped = append(extraction.skipped, githubTarImportSkip{Path: repoPath, Reason: "ignored"})
			continue
		}
		if header.Size > githubTarballMaxFileBytes {
			extraction.skipped = append(extraction.skipped, githubTarImportSkip{Path: repoPath, Reason: "too-large"})
			continue
		}
		content, err := io.ReadAll(io.LimitReader(tr, githubTarballMaxFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read tar entry %q: %w", repoPath, err)
		}
		if len(content) > githubTarballMaxFileBytes {
			extraction.skipped = append(extraction.skipped, githubTarImportSkip{Path: repoPath, Reason: "too-large"})
			continue
		}
		file := relayfile.BulkWriteFile{}
		if githubTarballLooksBinary(repoPath, content) {
			file.Content = base64.StdEncoding.EncodeToString(content)
			file.Encoding = "base64"
			file.ContentType = "application/octet-stream"
		} else {
			file.Content = string(content)
			file.Encoding = "utf8"
			file.ContentType = "text/plain"
		}
		extraction.entries = append(extraction.entries, githubTarballEntry{
			repoPath: repoPath,
			file:     file,
			size:     int64(len(content)),
		})
	}
	return extraction, nil
}

// ── import ──────────────────────────────────────────────────────────────────

func githubTarballWorkspacePath(owner, repo, repoPath string) string {
	return normalizeRoutePath("/github/repos/" + owner + "/" + repo + "/contents/" + repoPath)
}

// importGithubTarballEntries writes extracted entries into the workspace via
// the store bulk-write path so every file emits a filesystem event. Each file
// is permission-checked the same way the /fs/bulk endpoint checks writes.
func (s *Server) importGithubTarballEntries(workspaceID, owner, repo, headSha, ref, jobID, correlationID string, extraction *githubTarballExtraction, claims tokenClaims) githubTarImportSummary {
	summary := githubTarImportSummary{
		Errors:  []relayfile.BulkWriteError{},
		Skipped: extraction.skipped,
	}

	allowed := make([]relayfile.BulkWriteFile, 0, len(extraction.entries))
	sizeByPath := make(map[string]int64, len(extraction.entries))
	for _, entry := range extraction.entries {
		workspacePath := githubTarballWorkspacePath(owner, repo, entry.repoPath)
		if writeErr := s.githubTarballWritePermissionError(workspaceID, workspacePath, claims); writeErr != nil {
			summary.Errors = append(summary.Errors, relayfile.BulkWriteError{
				Path:    workspacePath,
				Code:    writeErr.Code,
				Message: writeErr.Message,
			})
			continue
		}
		file := entry.file
		file.Path = workspacePath
		allowed = append(allowed, file)
		sizeByPath[workspacePath] = entry.size
	}

	for start := 0; start < len(allowed); start += githubTarballBulkChunkSize {
		end := start + githubTarballBulkChunkSize
		if end > len(allowed) {
			end = len(allowed)
		}
		written, results, storeErrors := s.store.BulkWrite(workspaceID, allowed[start:end])
		summary.Imported += written
		summary.Errors = append(summary.Errors, storeErrors...)
		for _, result := range results {
			summary.BytesWritten += sizeByPath[result.Path]
		}
	}

	markerPath := githubTarballCloneMarkerPath(owner, repo)
	if writeErr := s.githubTarballWritePermissionError(workspaceID, markerPath, claims); writeErr != nil {
		summary.Errors = append(summary.Errors, relayfile.BulkWriteError{
			Path:    markerPath,
			Code:    writeErr.Code,
			Message: writeErr.Message,
		})
		return summary
	}
	s.writeGithubTarballCloneMarker(workspaceID, owner, repo, headSha, ref, jobID)
	return summary
}

func (s *Server) githubTarballWritePermissionError(workspaceID, workspacePath string, claims tokenClaims) *relayfile.BulkWriteError {
	_, readErr := s.readFile(workspaceID, "", workspacePath)
	var permissions []string
	if readErr == nil {
		permissions = s.resolveFilePermissions(workspaceID, "", workspacePath, true)
	} else if readErr == relayfile.ErrNotFound || readErr == relayfile.ErrForkExpired {
		permissions = s.resolveFilePermissions(workspaceID, "", workspacePath, false)
	} else {
		return &relayfile.BulkWriteError{
			Code:    "internal_error",
			Message: "failed to check file permissions",
		}
	}
	if !filePermissionAllows(permissions, workspaceID, &claims) {
		return &relayfile.BulkWriteError{
			Code:    "forbidden",
			Message: "file access denied by permission policy",
		}
	}
	return nil
}

func githubTarballCloneMarkerPath(owner, repo string) string {
	return normalizeRoutePath("/github/repos/" + owner + "/" + repo + "/.relayfile/clone.json")
}

// writeGithubTarballCloneMarker records the imported head SHA as workspace
// data. The marker is not counted in the import summary, but it does emit a
// normal file event like any other provider mutation.
func (s *Server) writeGithubTarballCloneMarker(workspaceID, owner, repo, headSha, ref, jobID string) {
	marker := map[string]string{
		"owner":      owner,
		"repo":       repo,
		"headSha":    headSha,
		"importedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if ref != "" {
		marker["ref"] = ref
	}
	if jobID != "" {
		marker["jobId"] = jobID
	}
	content, err := json.Marshal(marker)
	if err != nil {
		return
	}
	markerPath := githubTarballCloneMarkerPath(owner, repo)
	s.store.BulkWrite(workspaceID, []relayfile.BulkWriteFile{{
		Path:        markerPath,
		ContentType: "application/json",
		Content:     string(content),
		Encoding:    "utf8",
	}})
}
