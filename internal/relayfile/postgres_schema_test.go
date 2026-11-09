package relayfile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testPostgresContract() PostgresContract {
	digest := sha256.Sum256([]byte("public test CA"))
	return PostgresContract{Database: "relayfile", OwnerRole: "relayfile", RuntimeRole: "relayfile_runtime", OperatorRole: "postgres", CASHA256: hex.EncodeToString(digest[:])}
}

func TestStrictPostgresDSNAdmission(t *testing.T) {
	c := testPostgresContract()
	valid := "postgresql://relayfile_runtime:planted-secret@postgres.example:5432/relayfile?sslmode=verify-full&sslrootcert=/public/ca.crt"
	clean := func(string) (string, bool) { return "", false }
	ca := func(string) ([]byte, error) { return []byte("public test CA"), nil }
	got, err := validatePostgresDSN(valid, c, false, clean, ca)
	if err != nil || !strings.Contains(got, "search_path=pg_catalog%2Cpublic") {
		t.Fatalf("valid: %v", err)
	}
	cases := map[string]string{
		"tls":              strings.Replace(valid, "verify-full", "require", 1),
		"missing-password": strings.Replace(valid, ":planted-secret", "", 1),
		"owner-runtime":    strings.Replace(valid, "relayfile_runtime:", "relayfile:", 1),
		"database":         strings.Replace(valid, "/relayfile?", "/hoopdb?", 1),
		"options":          valid + "&options=-crole%3Drelayfile",
		"search-path":      valid + "&search_path=other",
		"duplicate-tls":    valid + "&sslmode=disable",
		"multi-host":       strings.Replace(valid, "postgres.example", "postgres.example,other", 1),
		"missing-port":     strings.Replace(valid, ":5432", "", 1),
		"relative-ca":      strings.Replace(valid, "/public/ca.crt", "ca.crt", 1),
		"bad-escape":       "postgresql://relayfile_runtime:planted-secret%zz@postgres.example:5432/relayfile",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := validatePostgresDSN(dsn, c, false, clean, ca)
			if err == nil || strings.Contains(err.Error(), "planted-secret") || strings.Contains(err.Error(), "postgres.example") {
				t.Fatalf("unsafe refusal: %v", err)
			}
		})
	}
	for _, name := range []string{"PGPASSWORD", "PGOPTIONS", "PGSSLMODE", "PGSERVICE", "PGREALM", "PGSSLSNI", "PGSSLCRL", "PGREQUIREPEER", "PGKRBSRVNAME", "PGGSSLIB", "PGSYSCONFDIR", "PGLOCALEDIR"} {
		t.Run(name, func(t *testing.T) {
			_, err := validatePostgresDSN(valid, c, false, func(k string) (string, bool) { return "planted-secret", k == name }, ca)
			if err == nil || err.Error() != "postgres_ambient_connection_override" {
				t.Fatalf("ambient override: %v", err)
			}
		})
	}
	c.CASHA256 = strings.Repeat("0", 64)
	if _, err := validatePostgresDSN(valid, c, false, clean, ca); err == nil || err.Error() != "postgres_ca_changed" {
		t.Fatalf("CA: %v", err)
	}
}

func TestPublicCAReaderBoundedProjectedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(path, []byte("public test CA"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "projected-ca.crt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if data, err := readPostgresCA(link); err != nil || string(data) != "public test CA" {
		t.Fatalf("projected CA: %v", err)
	}
	if _, err := readPostgresCA(dir); err == nil {
		t.Fatal("directory admitted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1024*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPostgresCA(path); err == nil {
		t.Fatal("unbounded CA admitted")
	}
}

type observedConnector struct{ calls int }

func (c *observedConnector) Driver() driver.Driver { return contractTestDriver{&contractFixture{}} }
func (c *observedConnector) Connect(context.Context) (driver.Conn, error) {
	c.calls++
	return &contractTestConn{&contractFixture{}}, nil
}

func TestBoundConnectorRechecksCAAndAmbientOverrides(t *testing.T) {
	c := testPostgresContract()
	dsn := "postgresql://relayfile_runtime:planted-secret@postgres.example:5432/relayfile?sslmode=verify-full&sslrootcert=/public/ca.crt"
	inner := &observedConnector{}
	caBytes := []byte("public test CA")
	override := false
	connector := &postgresBoundConnector{inner: inner, admit: func() error {
		_, err := validatePostgresDSN(dsn, c, false, func(name string) (string, bool) { return "planted-secret", override && name == "PGOPTIONS" }, func(string) ([]byte, error) { return caBytes, nil })
		return err
	}}
	conn, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	caBytes = []byte("other public CA")
	if _, err = connector.Connect(context.Background()); err == nil || err.Error() != "postgres_ca_changed" {
		t.Fatalf("changed CA: %v", err)
	}
	caBytes = []byte("public test CA")
	override = true
	if _, err = connector.Connect(context.Background()); err == nil || err.Error() != "postgres_ambient_connection_override" {
		t.Fatalf("later override: %v", err)
	}
	if inner.calls != 1 {
		t.Fatal("refused connection reached transport")
	}
}

func TestBootstrapSchemaHasNarrowSourceOwnedGrants(t *testing.T) {
	statements := postgresBootstrapStatements(testPostgresContract())
	joined := strings.Join(statements, ";\n")
	if strings.Count(joined, "CREATE TABLE ") != 3 || strings.Count(joined, "CREATE INDEX ") != 2 || strings.Count(joined, "GRANT UPDATE(id)") != 2 || strings.Count(joined, "GRANT USAGE ON SEQUENCE") != 2 {
		t.Fatal("unexpected schema/grant set")
	}
	for _, forbidden := range []string{"CREATE ROLE", "ALTER ROLE", "PASSWORD", "SUPERUSER", "GRANT ALL", "DROP ", "IF NOT EXISTS"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("unexpected bootstrap authority: %s", forbidden)
		}
	}
	if !strings.Contains(joined, `REVOKE CREATE,TEMPORARY ON DATABASE "relayfile" FROM PUBLIC,"relayfile_runtime"`) {
		t.Fatal("PUBLIC default TEMP remains effective")
	}
}

// This driver exercises the actual verifier's admission and transaction
// branches without a server. Native PG17 integration remains a separate gate.
type contractFixture struct {
	fail       string
	execs      int
	readOnly   bool
	rolledBack bool
	payloads   []driver.Value
}
type contractTestDriver struct{ fixture *contractFixture }
type contractTestConn struct{ fixture *contractFixture }
type contractTestTx struct{ fixture *contractFixture }
type contractTestRows struct {
	data    [][]driver.Value
	columns int
}

func (d contractTestDriver) Open(string) (driver.Conn, error) {
	return &contractTestConn{d.fixture}, nil
}
func (c *contractTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare_unexpected")
}
func (c *contractTestConn) Close() error              { return nil }
func (c *contractTestConn) Begin() (driver.Tx, error) { return nil, errors.New("begin_unexpected") }
func (c *contractTestConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	c.fixture.readOnly = o.ReadOnly
	return &contractTestTx{c.fixture}, nil
}
func (c *contractTestConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.fixture.execs++
	return nil, errors.New("write_forbidden")
}
func (t *contractTestTx) Commit() error   { return errors.New("commit_unexpected") }
func (t *contractTestTx) Rollback() error { t.fixture.rolledBack = true; return nil }
func (r *contractTestRows) Columns() []string {
	a := make([]string, r.columns)
	for i := range a {
		a[i] = fmt.Sprint(i)
	}
	return a
}
func (r *contractTestRows) Close() error { return nil }
func (r *contractTestRows) Next(out []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(out, r.data[0])
	r.data = r.data[1:]
	return nil
}
func fixtureRows(data ...[]driver.Value) driver.Rows {
	n := 1
	if len(data) > 0 {
		n = len(data[0])
	}
	return &contractTestRows{data: data, columns: n}
}
func (c *contractTestConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	f := c.fixture
	if strings.HasPrefix(q, "SELECT payload FROM") {
		if f.fail == "payload-query" {
			return nil, errors.New("planted raw driver detail")
		}
		data := make([][]driver.Value, 0, len(f.payloads))
		for _, payload := range f.payloads {
			data = append(data, []driver.Value{payload})
		}
		return fixtureRows(data...), nil
	}
	if strings.HasPrefix(q, "SELECT current_database()") {
		return fixtureRows([]driver.Value{"relayfile", "relayfile_runtime", f.fail != "tls", true, true, true, true, f.fail != "membership"}), nil
	}
	if strings.Contains(q, "has_database_privilege") {
		return fixtureRows([]driver.Value{f.fail != "ddl"}), nil
	}
	if strings.Contains(q, "c.relkind='r'") {
		return fixtureRows([]driver.Value{f.fail != "owner"}), nil
	}
	ref := ""
	if len(args) > 0 {
		ref, _ = args[0].Value.(string)
	}
	state := ref == "public.relayfile_state"
	if strings.HasPrefix(q, "SELECT a.attname,format_type") {
		if state {
			def := "now()"
			if f.fail == "column" {
				def = "other()"
			}
			return fixtureRows([]driver.Value{"state_key", "text", true, ""}, []driver.Value{"snapshot", "text", true, ""}, []driver.Value{"updated_at", "timestamp with time zone", true, def}), nil
		}
		name := strings.TrimPrefix(ref, "public.")
		return fixtureRows([]driver.Value{"id", "bigint", true, "nextval('" + name + "_id_seq'::regclass)"}, []driver.Value{"queue_key", "text", true, ""}, []driver.Value{"payload", "text", true, ""}, []driver.Value{"created_at", "timestamp with time zone", true, "now()"}), nil
	}
	if strings.Contains(q, "FROM pg_constraint") {
		return fixtureRows([]driver.Value{f.fail != "constraint"}), nil
	}
	if strings.Contains(q, "FROM pg_index") {
		return fixtureRows([]driver.Value{f.fail != "index"}), nil
	}
	if strings.HasPrefix(q, "SELECT has_table_privilege") {
		ref = args[1].Value.(string)
		state = ref == "public.relayfile_state"
		p := args[2].Value.(string)
		return fixtureRows([]driver.Value{p == "SELECT" || p == "INSERT" || (state && p == "UPDATE") || (!state && p == "DELETE") || (f.fail == "maintain" && p == "MAINTAIN")}), nil
	}
	if strings.Contains(q, "CROSS JOIN LATERAL aclexplode") {
		return fixtureRows([]driver.Value{f.fail != "acl"}), nil
	}
	if strings.HasPrefix(q, "SELECT attname,has_column_privilege") {
		if state {
			return fixtureRows([]driver.Value{"state_key", true, true}, []driver.Value{"snapshot", true, true}, []driver.Value{"updated_at", true, true}), nil
		}
		return fixtureRows([]driver.Value{"id", f.fail != "queue-lock", true}, []driver.Value{"queue_key", false, true}, []driver.Value{"payload", false, true}, []driver.Value{"created_at", false, true}), nil
	}
	if strings.Contains(q, "pg_get_serial_sequence") {
		return fixtureRows([]driver.Value{f.fail != "sequence"}), nil
	}
	return nil, errors.New("query_unexpected")
}

var contractDriverCounter uint64

func contractFixtureDB(t *testing.T, f *contractFixture) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("contract-fixture-%d", atomic.AddUint64(&contractDriverCounter, 1))
	sql.Register(name, contractTestDriver{f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPostgresVerifierReadOnlyRefusals(t *testing.T) {
	for _, failure := range []string{"", "tls", "membership", "ddl", "owner", "column", "constraint", "index", "acl", "queue-lock", "sequence", "maintain"} {
		t.Run(failure, func(t *testing.T) {
			fixture := &contractFixture{fail: failure}
			db := contractFixtureDB(t, fixture)
			err := verifyContractDB(context.Background(), db, testPostgresContract())
			if (err == nil) != (failure == "") {
				t.Fatalf("failure %q: %v", failure, err)
			}
			if !fixture.readOnly || !fixture.rolledBack || fixture.execs != 0 {
				t.Fatal("verifier escaped read-only transaction")
			}
		})
	}
}

type failedStateBackend struct{ calls int }

func (f *failedStateBackend) Load() (*persistedState, error) {
	f.calls++
	return nil, errors.New("planted snapshot content")
}
func (f *failedStateBackend) Save(*persistedState) error { return errors.New("save_unexpected") }

type observedStartupQueue struct {
	EnvelopeQueue
	snapshots int
}

func (q *observedStartupQueue) SnapshotEnvelopeIDs() []string { q.snapshots++; return nil }

func TestCheckedStoreLoadFailureBeforeQueueOrWorkers(t *testing.T) {
	backend := &failedStateBackend{}
	queue := &observedStartupQueue{EnvelopeQueue: NewInMemoryEnvelopeQueue(1)}
	store, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: backend, EnvelopeQueue: queue})
	if store != nil || err == nil || err.Error() != "persistent_state_load_failed" || backend.calls != 1 || queue.snapshots != 0 {
		t.Fatalf("checked startup leaked or continued: %v", err)
	}
	legacy := NewStoreWithOptions(StoreOptions{StateBackend: &failedStateBackend{}, DisableWorkers: true})
	if legacy == nil {
		t.Fatal("legacy compatibility changed")
	}
	legacy.Close()
	positive, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: NewInMemoryStateBackend(), DisableWorkers: true})
	if err != nil || positive == nil {
		t.Fatalf("checked positive: %v", err)
	}
	positive.Close()
}

func TestQualifiedBackendsCannotEnterLazyDDL(t *testing.T) {
	fixture := &contractFixture{}
	db := contractFixtureDB(t, fixture)
	b := &PostgresStateBackend{db: db, tableName: postgresStateTableName, qualified: true}
	b.initOnce.Do(func() {})
	q := &postgresQueueCore{db: db, tableName: postgresEnvelopeQueueTableName, qualified: true}
	q.initOnce.Do(func() {})
	if b.ensureReady() != nil || q.ensureReady() != nil || fixture.execs != 0 || b.tableReference() != `"public"."relayfile_state"` || q.tableReference() != `"public"."relayfile_envelope_queue"` {
		t.Fatal("strict backend entered legacy initialization")
	}
}

type failedStartupQueue struct {
	EnvelopeQueue
	snapshots int
}

func (q *failedStartupQueue) SnapshotEnvelopeIDsChecked() ([]string, error) {
	q.snapshots++
	return nil, errors.New("planted queue contents")
}

func TestCheckedStoreQueueFailureStopsStartup(t *testing.T) {
	queue := &failedStartupQueue{EnvelopeQueue: NewInMemoryEnvelopeQueue(1)}
	store, err := NewStoreWithOptionsChecked(StoreOptions{StateBackend: NewInMemoryStateBackend(), EnvelopeQueue: queue})
	if store != nil || err == nil || err.Error() != "persistent_queue_load_failed" || queue.snapshots != 1 {
		t.Fatalf("queue failure did not stop startup: %v", err)
	}
}

func TestCheckedPostgresQueueSnapshots(t *testing.T) {
	for _, test := range []struct {
		name, fail string
		payloads   []driver.Value
		want       string
	}{
		{name: "empty"},
		{name: "query-refusal", fail: "payload-query", want: "postgres_queue_unavailable"},
		{name: "corrupt-payload", payloads: []driver.Value{"planted contents"}, want: "postgres_queue_payload_invalid"},
		{name: "valid-payload", payloads: []driver.Value{`{"WorkspaceID":"workspace","OpID":"operation"}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &contractFixture{fail: test.fail, payloads: test.payloads}
			core := &postgresQueueCore{db: contractFixtureDB(t, fixture), tableName: postgresWritebackQueueTableName, queueKey: "default", qualified: true}
			core.initOnce.Do(func() {})
			items, err := (&PostgresWritebackQueue{core: core}).SnapshotWritebacksChecked()
			if test.want != "" {
				if err == nil || err.Error() != test.want {
					t.Fatalf("unsafe queue refusal: %v", err)
				}
			} else if err != nil || len(items) != len(test.payloads) {
				t.Fatalf("queue snapshot: %v", err)
			}
			if fixture.execs != 0 {
				t.Fatal("checked snapshot performed DDL")
			}
		})
	}
}
