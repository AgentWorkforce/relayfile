package main

import (
	"strings"
	"testing"
)

func TestIntegrationMetadataFileRefusesUnvalidatedWindowsACL(t *testing.T) {
	_, err := readIntegrationMetadataFile("unused.json")
	if err == nil || !strings.Contains(err.Error(), "Windows ACL validation is not supported") {
		t.Fatalf("expected explicit ACL policy refusal, got %v", err)
	}
}
