package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func postgresCLIInput(t *testing.T) ([]string, string) {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, []byte("public test CA"), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("public test CA"))
	args := []string{"postgres", "serve", "--database", "relayfile", "--owner-role", "relayfile", "--runtime-role", "relayfile_runtime", "--ca-sha256", hex.EncodeToString(h[:])}
	dsn := "postgresql://relayfile_runtime:planted-secret@postgres.example:5432/relayfile?sslmode=verify-full&sslrootcert=" + ca
	return args, dsn
}

func TestPostgresCommandProjectedSecretAndStdin(t *testing.T) {
	args, dsn := postgresCLIInput(t)
	cmd, err := parsePostgresCommand(append(args, "--dsn-stdin"), strings.NewReader(dsn+"\n"))
	if err != nil || cmd == nil || cmd.dsn != dsn {
		t.Fatalf("stdin: %v", err)
	}
	file := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(file, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	cmd, err = parsePostgresCommand(append(args, "--dsn-file", file), nil)
	if err != nil || cmd == nil || cmd.dsn != dsn {
		t.Fatalf("projected file: %v", err)
	}
	_, err = parsePostgresCommand(append(args, "--dsn-file", file, "--dsn-stdin"), strings.NewReader(dsn))
	if err == nil {
		t.Fatal("ambiguous credential source admitted")
	}
	for _, operation := range []string{"bootstrap", "verify"} {
		other := append([]string(nil), args...)
		other[1] = operation
		_, err = parsePostgresCommand(append(other, "--dsn-file", file), nil)
		if err == nil {
			t.Fatal("operator file credential source admitted")
		}
	}
}

type unexpectedCredentialRead struct{ read bool }

func (r *unexpectedCredentialRead) Read([]byte) (int, error) { r.read = true; return 0, io.EOF }

func TestPostgresCommandRefusalsNeverEchoCredential(t *testing.T) {
	args, dsn := postgresCLIInput(t)
	for _, extra := range [][]string{{"--dsn", "planted-secret"}, {"--dsn-file", "relative"}, {"--dsn-file", "/nonexistent/planted-secret"}, {"--dsn-stdin", "extra-planted-secret"}} {
		reader := &unexpectedCredentialRead{}
		_, err := parsePostgresCommand(append(args, extra...), reader)
		if err == nil || strings.Contains(err.Error(), "planted-secret") || reader.read {
			t.Fatalf("unsafe options: %v", err)
		}
	}
	_, err := parsePostgresCommand(append(args, "--dsn-stdin"), strings.NewReader(strings.Repeat("planted-secret", 2000)))
	if err == nil || strings.Contains(err.Error(), "planted-secret") {
		t.Fatalf("unbounded input: %v", err)
	}
	file := filepath.Join(t.TempDir(), "bad-dsn")
	if err = os.WriteFile(file, []byte(strings.Replace(dsn, "planted-secret", "planted-secret%zz", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = parsePostgresCommand(append(args, "--dsn-file", file), nil)
	if err == nil || strings.Contains(err.Error(), "planted-secret") {
		t.Fatalf("URL parser leaked credential: %v", err)
	}
	if cmd, err := parsePostgresCommand(nil, nil); cmd != nil || err != nil {
		t.Fatal("default serving mode changed")
	}
}

func TestStrictServingRefusesLegacyTargetOverridesBeforeConnection(t *testing.T) {
	for _, name := range []string{"RELAYFILE_STATE_BACKEND_DSN", "RELAYFILE_PRODUCTION_DSN", "RELAYFILE_WRITEBACK_QUEUE_FILE", "RELAYFILE_POSTGRES_DSN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "") // Presence itself is ambiguous, including empty values.
			_, _, _, err := buildVerifiedStorage(&postgresCommand{})
			if err == nil || err.Error() != "postgres_storage_override_refused" {
				t.Fatalf("override was not rejected before connection: %v", err)
			}
		})
	}
}
