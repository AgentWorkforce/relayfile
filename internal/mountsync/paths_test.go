package mountsync

import (
	"path/filepath"
	"strings"
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
		{"leading-space", " doc.md", "mount", " doc.md"},
		{"trailing-space", "doc.md ", "mount", "doc.md "},
		{"spaceful-basename", "mount", "mount ", "mount"},
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

// TestNewRelativeRemotePath_RejectsNUL pins the NUL-byte safeguard.
// Some libc / syscall paths silently truncate at the first NUL,
// turning "ok.md\x00/../../etc/passwd" into a confused-deputy hazard.
// The constructor must reject any NUL-containing path up-front.
func TestNewRelativeRemotePath_RejectsNUL(t *testing.T) {
	cases := []string{
		"\x00",
		"ok.md\x00",
		"\x00ok.md",
		"sub\x00/doc.md",
		"sub/\x00doc.md",
	}
	for _, rel := range cases {
		if _, err := NewRelativeRemotePath(rel, "mount"); err == nil {
			t.Fatalf("expected NUL-byte rejection for %q", rel)
		}
	}
}

// TestNewRelativeRemotePath_RejectsOversized pins the length cap.
// A misbehaving provider returning a multi-KB path must be rejected
// at construction so the rest of the pipeline never has to defend
// against it.
func TestNewRelativeRemotePath_RejectsOversized(t *testing.T) {
	// One byte over the cap must fail.
	tooLong := strings.Repeat("a", MaxRemotePathLen+1)
	if _, err := NewRelativeRemotePath(tooLong, "mount"); err == nil {
		t.Fatalf("expected oversize rejection for %d-byte path", len(tooLong))
	}

	// Exactly at the cap must still succeed (the cap is inclusive).
	atCap := strings.Repeat("a", MaxRemotePathLen)
	if _, err := NewRelativeRemotePath(atCap, "mount"); err != nil {
		t.Fatalf("path at exactly %d bytes was rejected: %v", MaxRemotePathLen, err)
	}

	// A reasonably long nested path well under the cap stays accepted.
	reasonable := strings.Repeat("sub/", 100) + "doc.md"
	if _, err := NewRelativeRemotePath(reasonable, "mount"); err != nil {
		t.Fatalf("reasonable nested path rejected: %v", err)
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
