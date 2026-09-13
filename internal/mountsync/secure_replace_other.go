//go:build aix || solaris || windows || plan9 || js

package mountsync

import (
	"errors"
	"os"
)

// These systems do not expose the directory-fd primitives needed to make an
// ancestor-swap-safe replacement. Callers fail closed instead of falling back
// to a check-then-open sequence that could escape the mount root.
var errAncestorSafetyUnsupported = errors.New("secure ancestor operations are unsupported on this platform")

func ensureSecureParentDirectory(root, target string) error { return errAncestorSafetyUnsupported }

func writeFileAtomicSecure(root, target string, data []byte, mode os.FileMode) error {
	return errAncestorSafetyUnsupported
}

func writeSymlinkAtomicSecure(root, targetPath, target string) error {
	return errAncestorSafetyUnsupported
}
