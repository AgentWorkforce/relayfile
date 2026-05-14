package mountfuse

import (
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	"github.com/agentworkforce/relayfile/schemas"
)

const (
	schemaFilename    = ".schema.json"
	schemaRevision    = "virtual-resource-schema"
	schemaContentType = "application/json"
)

var bundledResourceSchemas = map[string]string{
	"github/issue": "github/issue.schema.json",
}

func isReadOnlyVirtualPath(remoteRoot, remotePath string) bool {
	if isVirtualLayoutPath(remoteRoot, remotePath) {
		return true
	}
	if _, ok := isVirtualProviderLayoutPath(remoteRoot, remotePath); ok {
		return true
	}
	if _, _, ok := isVirtualSchemaPath(remoteRoot, remotePath); ok {
		return true
	}
	if isDeadLetterErrorSidecarPath(remoteRoot, remotePath) {
		return true
	}
	return false
}

// isDeadLetterErrorSidecarPath matches `.relay/dead-letter/*.error.json` files,
// which carry the structured failure cause for a parked writeback and must
// stay read-only at the FUSE layer.
func isDeadLetterErrorSidecarPath(remoteRoot, remotePath string) bool {
	rel, ok := relativeToRemoteRoot(remoteRoot, remotePath)
	if !ok || rel == "" {
		return false
	}
	if !strings.HasPrefix(rel, ".relay/dead-letter/") {
		return false
	}
	base := path.Base(rel)
	return strings.HasSuffix(base, ".error.json") && base != ".error.json"
}

func schemaRemotePath(parentPath string) string {
	return joinRemotePath(parentPath, schemaFilename)
}

func isVirtualSchemaPath(remoteRoot, remotePath string) (provider, resource string, ok bool) {
	remotePath = normalizeRemotePath(remotePath)
	if path.Base(remotePath) != schemaFilename {
		return "", "", false
	}
	parentPath, _ := splitParent(remotePath)
	return virtualSchemaForDirectory(remoteRoot, parentPath)
}

func virtualSchemaForDirectory(remoteRoot, remotePath string) (provider, resource string, ok bool) {
	rel, ok := relativeToRemoteRoot(remoteRoot, remotePath)
	if !ok || rel == "" {
		return "", "", false
	}
	segments := strings.Split(rel, "/")
	if len(segments) < 2 {
		return "", "", false
	}
	provider = segments[0]
	resourceDir := segments[len(segments)-1]
	resource = schemaResourceName(provider, resourceDir)
	if resource == "" {
		return "", "", false
	}
	return provider, resource, true
}

func virtualSchemaMeta(parentPath, provider, resource string, payload []byte) nodeMeta {
	return nodeMeta{
		path:        schemaRemotePath(parentPath),
		name:        schemaFilename,
		mode:        syscall.S_IFREG | 0o444,
		revision:    schemaRevision,
		size:        uint64(len(payload)),
		modTime:     time.Unix(0, 0).UTC(),
		contentType: schemaContentType,
	}
}

func readVirtualSchema(parentPath, provider, resource string, payload []byte) mountsync.RemoteFile {
	meta := virtualSchemaMeta(parentPath, provider, resource, payload)
	return mountsync.RemoteFile{
		Path:        meta.path,
		Revision:    meta.revision,
		ContentType: meta.contentType,
		Content:     string(payload),
	}
}

func loadResourceSchema(provider, resource string) ([]byte, bool) {
	key := strings.TrimSpace(provider) + "/" + strings.TrimSpace(resource)
	schemaPath, ok := bundledResourceSchemas[key]
	if !ok {
		return nil, false
	}
	payload, err := schemas.FS.ReadFile(schemaPath)
	if err != nil {
		return nil, false
	}
	return payload, true
}

func schemaResourceName(provider, resourceDir string) string {
	provider = strings.TrimSpace(provider)
	resourceDir = strings.Trim(strings.TrimSpace(resourceDir), "/")
	switch provider + "/" + resourceDir {
	case "github/issues":
		return "issue"
	default:
		return ""
	}
}
