package mountfuse

import (
	"syscall"
	"testing"
)

func TestIsVirtualSchemaPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteRoot string
		remotePath string
		provider   string
		resource   string
		wantOK     bool
	}{
		{name: "github issues", remoteRoot: "/", remotePath: "/github/repos/octocat/hello-world/issues/.schema.json", provider: "github", resource: "issue", wantOK: true},
		{name: "prefixed root", remoteRoot: "/external", remotePath: "/external/github/repos/octocat/hello-world/issues/.schema.json", provider: "github", resource: "issue", wantOK: true},
		{name: "unknown provider", remoteRoot: "/", remotePath: "/linear/issues/.schema.json", wantOK: false},
		{name: "wrong filename", remoteRoot: "/", remotePath: "/github/repos/octocat/hello-world/issues/schema.json", wantOK: false},
		{name: "outside root", remoteRoot: "/external", remotePath: "/github/repos/octocat/hello-world/issues/.schema.json", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, resource, ok := isVirtualSchemaPath(tt.remoteRoot, tt.remotePath)
			if ok != tt.wantOK || provider != tt.provider || resource != tt.resource {
				t.Fatalf("isVirtualSchemaPath(%q, %q) = (%q, %q, %v), want (%q, %q, %v)", tt.remoteRoot, tt.remotePath, provider, resource, ok, tt.provider, tt.resource, tt.wantOK)
			}
		})
	}
}

func TestVirtualSchemaMetaReadOnly(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema"}`)
	meta := virtualSchemaMeta("/github/repos/octocat/hello-world/issues", "github", "issue", payload)
	if meta.mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Fatalf("schema mode = %o, want regular file", meta.mode)
	}
	if perm := meta.mode & 0o777; perm != 0o444 {
		t.Fatalf("schema permissions = %o, want 0444", perm)
	}
	if meta.size != uint64(len(payload)) {
		t.Fatalf("schema size = %d, want %d", meta.size, len(payload))
	}
}
