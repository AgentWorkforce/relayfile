//go:build darwin

package mountsync

// macOS presents these root-owned compatibility aliases in the standard
// filesystem namespace. They are the only intermediate symlinks accepted by
// the anchored walk; arbitrary aliases remain fail-closed.
func allowIntermediateDirectorySymlink(name, target string) bool {
	switch {
	case name == "var" && target == "private/var":
		return true
	case name == "tmp" && target == "private/tmp":
		return true
	case name == "etc" && target == "private/etc":
		return true
	default:
		return false
	}
}
