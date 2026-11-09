package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

// Credentials are read only after all public options have been parsed. No
// driver/URL/parser error, file path or credential is included in diagnostics.
type postgresCommand struct {
	action   string
	dsn      string
	contract relayfile.PostgresContract
}

func parsePostgresCommand(args []string, input io.Reader) (*postgresCommand, error) {
	if len(args) == 0 || args[0] != "postgres" {
		return nil, nil
	}
	if len(args) < 2 || (args[1] != "bootstrap" && args[1] != "verify" && args[1] != "serve") {
		return nil, errors.New("postgres_invalid_command")
	}
	cmd := &postgresCommand{action: args[1]}
	flags := flag.NewFlagSet("postgres", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cmd.contract.Database, "database", "", "expected database")
	flags.StringVar(&cmd.contract.OwnerRole, "owner-role", "", "NOLOGIN schema owner")
	flags.StringVar(&cmd.contract.RuntimeRole, "runtime-role", "", "restricted runtime identity")
	flags.StringVar(&cmd.contract.OperatorRole, "operator-role", "", "bootstrap operator identity")
	flags.StringVar(&cmd.contract.CASHA256, "ca-sha256", "", "public CA SHA256")
	var fromStdin bool
	var file string
	flags.BoolVar(&fromStdin, "dsn-stdin", false, "read credential URL from stdin")
	flags.StringVar(&file, "dsn-file", "", "read serving credential from projected Secret file")
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 || fromStdin == (file != "") || (file != "" && (cmd.action != "serve" || !filepath.IsAbs(file))) {
		return nil, errors.New("postgres_invalid_options")
	}
	if cmd.action != "bootstrap" && cmd.contract.OperatorRole != "" {
		return nil, errors.New("postgres_invalid_operator")
	}
	reader := input
	if file != "" {
		stat, err := os.Stat(file)
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 16384 {
			return nil, errors.New("postgres_credential_unavailable")
		}
		f, err := os.Open(file)
		if err != nil {
			return nil, errors.New("postgres_credential_unavailable")
		}
		defer f.Close()
		stat, err = f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return nil, errors.New("postgres_credential_unavailable")
		}
		reader = f
	}
	if reader == nil {
		return nil, errors.New("postgres_credential_unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(reader, 16385))
	if err != nil || len(data) > 16384 {
		return nil, errors.New("postgres_credential_unavailable")
	}
	// A Secret may end with one newline; embedded whitespace remains invalid.
	cmd.dsn = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if _, err = relayfile.ValidatePostgresDSN(cmd.dsn, cmd.contract, cmd.action == "bootstrap"); err != nil {
		return nil, err
	}
	return cmd, nil
}

func executePostgresCommand(cmd *postgresCommand, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	if cmd.action == "bootstrap" {
		err = relayfile.BootstrapPostgres(ctx, cmd.dsn, cmd.contract)
	} else {
		err = relayfile.VerifyPostgres(ctx, cmd.dsn, cmd.contract)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"schemaVersion": 1, "result": "PASS", "operation": cmd.action})
}

func buildVerifiedStorage(cmd *postgresCommand) (relayfile.StateBackend, relayfile.EnvelopeQueue, relayfile.WritebackQueue, error) {
	// An explicit strict command must never silently select a memory/file or
	// second PostgreSQL target from the legacy independent override settings.
	for _, name := range []string{"RELAYFILE_STATE_BACKEND_DSN", "RELAYFILE_STATE_FILE", "RELAYFILE_ENVELOPE_QUEUE_DSN", "RELAYFILE_WRITEBACK_QUEUE_DSN", "RELAYFILE_ENVELOPE_QUEUE_FILE", "RELAYFILE_WRITEBACK_QUEUE_FILE", "RELAYFILE_PRODUCTION_DSN", "RELAYFILE_POSTGRES_DSN"} {
		if _, set := os.LookupEnv(name); set {
			return nil, nil, nil, errors.New("postgres_storage_override_refused")
		}
	}
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("RELAYFILE_BACKEND_PROFILE")))
	if profile != "" && profile != "production" && profile != "prod" {
		return nil, nil, nil, errors.New("postgres_storage_override_refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return relayfile.NewVerifiedPostgresBackends(ctx, cmd.dsn, cmd.contract, intEnv("RELAYFILE_ENVELOPE_QUEUE_SIZE", 0), intEnv("RELAYFILE_WRITEBACK_QUEUE_SIZE", 1024))
}
