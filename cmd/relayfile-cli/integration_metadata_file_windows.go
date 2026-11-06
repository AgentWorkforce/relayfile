package main

import (
	"errors"
	"os"
)

func openIntegrationMetadataFile(path string) (*os.File, error) {
	// Unix mode bits do not validate Windows ACLs. Refuse secret-bearing files
	// until descriptor-based ACL validation is available on this platform.
	return nil, errors.New("--metadata-file requires private Unix file permissions; Windows ACL validation is not supported")
}
