//go:build !darwin

package mountsync

func allowIntermediateDirectorySymlink(name, target string) bool {
	return false
}
