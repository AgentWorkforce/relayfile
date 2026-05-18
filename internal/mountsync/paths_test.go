package mountsync

import (
	"path/filepath"
	"testing"
)

func TestNewRelativeRemotePath_Rejects(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		base string
	}{
		{"empty", "", "mount"},
		{"dot", ".", "mount"},
		{"slash", "/", "mount"},
		{"leading-slash", "/foo.txt", "mount"},
		{"parent-traversal", "../escape.txt", "mount"},
		{"embedded-traversal", "sub/../../escape", "mount"},
		{"collides-with-basename", "mount", "mount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRelativeRemotePath(tc.rel, tc.base); err == nil {
				t.Fatalf("expected rejection for %q", tc.rel)
			}
		})
	}
}

func TestNewRelativeRemotePath_Accepts(t *testing.T) {
	cases := []struct {
		name, rel, base, want string
	}{
		{"plain-file", "doc.md", "mount", "doc.md"},
		{"nested", "sub/doc.md", "mount", "sub/doc.md"},
		{"leading-dot-slash", "./sub/doc.md", "mount", "sub/doc.md"},
		{"nested-with-basename-segment", "sub/mount/doc.md", "mount", "sub/mount/doc.md"},
		{"backslash-input", filepath.Join("sub", "doc.md"), "mount", "sub/doc.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewRelativeRemotePath(tc.rel, tc.base)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.rel, err)
			}
			if p.Slash() != tc.want {
				t.Fatalf("got %q want %q", p.Slash(), tc.want)
			}
		})
	}
}

func TestRelativeRemotePathFromLocal_RejectsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mount")
	if _, err := RelativeRemotePathFromLocal(root, root); err == nil {
		t.Fatalf("expected rejection when localPath == localRoot")
	}
	if _, err := RelativeRemotePathFromLocal(root, filepath.Join(root, "mount")); err == nil {
		t.Fatalf("expected rejection for child whose name equals mount basename")
	}
	if _, err := RelativeRemotePathFromLocal(root, filepath.Join(root, "ok.md")); err != nil {
		t.Fatalf("legitimate child rejected: %v", err)
	}
}
