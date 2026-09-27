package mountsync

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var objectHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type objectCache struct{ root string }

func defaultObjectCache() *objectCache {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return &objectCache{root: filepath.Join(home, ".relayfile", "cache", "objects")}
}

func normalizeObjectHash(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	if !objectHashPattern.MatchString(value) {
		return ""
	}
	return value
}

func (c *objectCache) get(hash string) (RemoteFile, bool) {
	var file RemoteFile
	hash = normalizeObjectHash(hash)
	if c == nil || hash == "" {
		return file, false
	}
	data, err := os.ReadFile(filepath.Join(c.root, hash))
	if err != nil || json.Unmarshal(data, &file) != nil || normalizeObjectHash(file.ContentHash) != hash || remoteFileHash(file) != hash {
		return RemoteFile{}, false
	}
	return file, true
}

func (c *objectCache) put(file RemoteFile) {
	hash := normalizeObjectHash(file.ContentHash)
	if c == nil || hash == "" || remoteFileHash(file) != hash {
		return
	}
	if os.MkdirAll(c.root, 0o700) != nil {
		return
	}
	data, err := json.Marshal(file)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(c.root, ".object-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		_ = os.Rename(name, filepath.Join(c.root, hash))
	}
}

func remoteFileHash(file RemoteFile) string {
	content := []byte(file.Content)
	if strings.EqualFold(file.Encoding, "base64") {
		decoded, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return ""
		}
		content = decoded
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
