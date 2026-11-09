package relayfile

// This contract is shared by the explicit owner bootstrap and the read-only
// runtime admission path. Legacy PostgreSQL constructors keep their old lazy
// initialization behavior; verified constructors never execute schema DDL.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

type PostgresContract struct {
	Database     string
	OwnerRole    string
	RuntimeRole  string
	OperatorRole string // required only by bootstrap; never the serving identity
	CASHA256     string
}

var postgresName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
var postgresSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Public errors contain literal reason codes only. Driver errors can include
// URLs, credentials or persisted content and must not cross this boundary.
func postgresRefuse(code string) error { return errors.New("postgres_" + code) }

func (c PostgresContract) validate(bootstrap bool) error {
	if !postgresName.MatchString(c.Database) || !postgresName.MatchString(c.OwnerRole) ||
		!postgresName.MatchString(c.RuntimeRole) || c.OwnerRole == c.RuntimeRole || !postgresSHA.MatchString(c.CASHA256) {
		return postgresRefuse("invalid_contract")
	}
	if bootstrap && (!postgresName.MatchString(c.OperatorRole) || c.OperatorRole == c.OwnerRole || c.OperatorRole == c.RuntimeRole) {
		return postgresRefuse("invalid_operator")
	}
	return nil
}

// ValidatePostgresDSN requires an explicit target and public CA, avoiding
// lib/pq's ambient connection defaults. It never returns the supplied URL.
func ValidatePostgresDSN(dsn string, c PostgresContract, bootstrap bool) (string, error) {
	return validatePostgresDSN(dsn, c, bootstrap, os.LookupEnv, readPostgresCA)
}

func readPostgresCA(path string) ([]byte, error) {
	// Projected Kubernetes files may be symlinks, but the resolved object must
	// be a bounded regular file rather than a device, directory or FIFO.
	stat, err := os.Stat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1024*1024 {
		return nil, postgresRefuse("ca_unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, postgresRefuse("ca_unavailable")
	}
	defer f.Close()
	stat, err = f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, postgresRefuse("ca_unavailable")
	}
	return io.ReadAll(io.LimitReader(f, 1024*1024+1))
}

func validatePostgresDSN(dsn string, c PostgresContract, bootstrap bool, lookup func(string) (string, bool), readFile func(string) ([]byte, error)) (string, error) {
	if err := c.validate(bootstrap); err != nil {
		return "", err
	}
	for _, name := range []string{"PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSERVICEFILE", "PGOPTIONS", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGREQUIRESSL", "PGCONNECT_TIMEOUT", "PGAPPNAME", "PGCLIENTENCODING", "PGDATESTYLE", "PGTZ", "PGGEQO", "PGREALM", "PGSSLSNI", "PGSSLCRL", "PGREQUIREPEER", "PGKRBSRVNAME", "PGGSSLIB", "PGSYSCONFDIR", "PGLOCALEDIR"} {
		if _, set := lookup(name); set {
			return "", postgresRefuse("ambient_connection_override")
		}
	}
	if len(dsn) == 0 || len(dsn) > 16384 || strings.TrimSpace(dsn) != dsn {
		return "", postgresRefuse("invalid_dsn")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" || u.User == nil || u.Hostname() == "" || strings.ContainsAny(u.Hostname(), ",/\\ \t\r\n") || u.Path != "/"+c.Database || u.RawPath != "" {
		return "", postgresRefuse("invalid_dsn")
	}
	wantUser := c.RuntimeRole
	if bootstrap {
		wantUser = c.OperatorRole
	}
	password, hasPassword := u.User.Password()
	port, err := strconv.Atoi(u.Port())
	if u.User.Username() != wantUser || !hasPassword || password == "" || err != nil || port < 1 || port > 65535 {
		return "", postgresRefuse("wrong_target")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", postgresRefuse("invalid_dsn")
	}
	for k, v := range query {
		if len(v) != 1 || (k != "sslmode" && k != "sslrootcert" && k != "connect_timeout") {
			return "", postgresRefuse("connection_option_refused")
		}
	}
	ca := query.Get("sslrootcert")
	if query.Get("sslmode") != "verify-full" || !filepath.IsAbs(ca) {
		return "", postgresRefuse("strict_tls_required")
	}
	caBytes, err := readFile(ca)
	if err != nil || len(caBytes) == 0 || len(caBytes) > 1024*1024 {
		return "", postgresRefuse("ca_unavailable")
	}
	digest := sha256.Sum256(caBytes)
	if hex.EncodeToString(digest[:]) != c.CASHA256 {
		return "", postgresRefuse("ca_changed")
	}
	if v := query.Get("connect_timeout"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 30 {
			return "", postgresRefuse("invalid_timeout")
		}
	} else {
		query.Set("connect_timeout", "5")
	}
	// Prevent inherited search paths from selecting relations outside this
	// contract. Application relations below are also explicitly qualified.
	query.Set("search_path", "pg_catalog,public")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func openContractDB(ctx context.Context, dsn string, c PostgresContract, bootstrap bool) (*sql.DB, error) {
	validated, err := ValidatePostgresDSN(dsn, c, bootstrap)
	if err != nil {
		return nil, err
	}
	connector, err := pq.NewConnector(validated)
	if err != nil {
		return nil, postgresRefuse("connection_failed")
	}
	// pq's connector freezes parsed connection settings, instead of parsing
	// ambient defaults again whenever database/sql opens another connection.
	// Recheck the bound public CA and override guard on each pool connection.
	db := sql.OpenDB(&postgresBoundConnector{inner: connector, admit: func() error {
		_, err := ValidatePostgresDSN(dsn, c, bootstrap)
		return err
	}})
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, postgresRefuse("connection_failed")
	}
	return db, nil
}

type postgresBoundConnector struct {
	inner driver.Connector
	admit func() error
}

func (c *postgresBoundConnector) Driver() driver.Driver { return c.inner.Driver() }
func (c *postgresBoundConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := c.admit(); err != nil {
		return nil, err
	}
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, postgresRefuse("connection_failed")
	}
	return conn, nil
}

// VerifyPostgres performs catalog/identity/privilege checks in a read-only
// transaction. It does not run DDL, grants, writes or the owner's bootstrap.
func VerifyPostgres(ctx context.Context, dsn string, c PostgresContract) error {
	db, err := openContractDB(ctx, dsn, c, false)
	if err != nil {
		return err
	}
	defer db.Close()
	return verifyContractDB(ctx, db, c)
}

func verifyContractDB(ctx context.Context, db *sql.DB, c PostgresContract) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return postgresRefuse("verification_failed")
	}
	defer tx.Rollback()
	if err = verifyPostgresIdentity(ctx, tx, c, false); err != nil {
		return err
	}
	return verifyPostgresObjects(ctx, tx, c)
}

// NewVerifiedPostgresBackends admits one fully specified runtime identity for
// all three backends before returning any of them. None can enter lazy DDL.
func NewVerifiedPostgresBackends(ctx context.Context, dsn string, c PostgresContract, envelopeCapacity, writebackCapacity int) (StateBackend, EnvelopeQueue, WritebackQueue, error) {
	db, err := openContractDB(ctx, dsn, c, false)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = verifyContractDB(ctx, db, c); err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	state := &PostgresStateBackend{tableName: postgresStateTableName, stateKey: postgresStateKey, db: db, qualified: true}
	state.initOnce.Do(func() {})
	newCore := func(table string, capacity int) *postgresQueueCore {
		if capacity <= 0 {
			capacity = 1024
		}
		q := &postgresQueueCore{tableName: table, queueKey: postgresQueueKey, capacity: capacity, pollInterval: postgresQueuePollInterval, db: db, qualified: true}
		q.initOnce.Do(func() {})
		return q
	}
	return state, &PostgresEnvelopeQueue{core: newCore(postgresEnvelopeQueueTableName, envelopeCapacity)}, &PostgresWritebackQueue{core: newCore(postgresWritebackQueueTableName, writebackCapacity)}, nil
}

func verifyPostgresIdentity(ctx context.Context, tx *sql.Tx, c PostgresContract, bootstrap bool) error {
	wantUser := c.RuntimeRole
	if bootstrap {
		wantUser = c.OperatorRole
	}
	var database, user string
	var ssl, ownerBound, ownerRestricted, runtimeRestricted, runtimeLogin, noMembership bool
	err := tx.QueryRowContext(ctx, `SELECT current_database(), current_user,
 COALESCE((SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()),false),
 (SELECT d.datdba=o.oid FROM pg_database d,pg_roles o WHERE d.datname=current_database() AND o.rolname=$1),
 (SELECT NOT (rolcanlogin OR rolinherit OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication) FROM pg_roles WHERE rolname=$1),
 (SELECT NOT (rolinherit OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication) FROM pg_roles WHERE rolname=$2),
 (SELECT rolcanlogin FROM pg_roles WHERE rolname=$2),
 NOT EXISTS(SELECT 1 FROM pg_roles r WHERE r.rolname<>$2 AND pg_has_role($2,r.oid,'MEMBER'))`, c.OwnerRole, c.RuntimeRole).Scan(&database, &user, &ssl, &ownerBound, &ownerRestricted, &runtimeRestricted, &runtimeLogin, &noMembership)
	if err != nil || database != c.Database || user != wantUser || !ssl || !ownerBound || !ownerRestricted || !runtimeRestricted || !noMembership || (!bootstrap && !runtimeLogin) {
		return postgresRefuse("identity_refused")
	}
	return nil
}

func tableRef(name string) string { return `"public".` + postgresQuoteIdentifier(name) }

// BootstrapPostgres is the only schema-writing path. A fresh empty public
// schema is created as the already-owned NOLOGIN role; existing installations
// are verification-only and incompatible/foreign objects are never repaired.
func BootstrapPostgres(ctx context.Context, dsn string, c PostgresContract) error {
	db, err := openContractDB(ctx, dsn, c, true)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return postgresRefuse("bootstrap_failed")
	}
	defer tx.Rollback()
	if err = verifyPostgresIdentity(ctx, tx, c, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL ROLE "+postgresQuoteIdentifier(c.OwnerRole)); err != nil {
		return postgresRefuse("owner_authority_refused")
	}
	// Transaction-scoped lock prevents two official bootstraps from racing.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(164779,1)"); err != nil {
		return postgresRefuse("bootstrap_failed")
	}
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','S','v','m','f')`).Scan(&existing); err != nil {
		return postgresRefuse("catalog_unavailable")
	}
	if existing == 0 {
		for _, statement := range postgresBootstrapStatements(c) {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return postgresRefuse("bootstrap_failed")
			}
		}
	}
	if err = verifyPostgresObjects(ctx, tx, c); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return postgresRefuse("bootstrap_failed")
	}
	return nil
}

func postgresBootstrapStatements(c PostgresContract) []string {
	runtime := postgresQuoteIdentifier(c.RuntimeRole)
	statements := []string{
		"REVOKE CREATE,TEMPORARY ON DATABASE " + postgresQuoteIdentifier(c.Database) + " FROM PUBLIC," + runtime,
		"GRANT CONNECT ON DATABASE " + postgresQuoteIdentifier(c.Database) + " TO " + runtime,
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC," + runtime,
		"GRANT USAGE ON SCHEMA public TO " + runtime,
		"CREATE TABLE " + tableRef(postgresStateTableName) + " (state_key TEXT PRIMARY KEY,snapshot TEXT NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())",
		"GRANT SELECT,INSERT,UPDATE ON " + tableRef(postgresStateTableName) + " TO " + runtime,
	}
	for _, name := range []string{postgresEnvelopeQueueTableName, postgresWritebackQueueTableName} {
		statements = append(statements,
			"CREATE TABLE "+tableRef(name)+" (id BIGSERIAL PRIMARY KEY,queue_key TEXT NOT NULL,payload TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())",
			"CREATE INDEX "+postgresQuoteIdentifier(name+"_queue_key_id_idx")+" ON "+tableRef(name)+" (queue_key,id)",
			"GRANT SELECT,INSERT,DELETE ON "+tableRef(name)+" TO "+runtime,
			"GRANT UPDATE(id) ON "+tableRef(name)+" TO "+runtime,
			"GRANT USAGE ON SEQUENCE "+tableRef(name+"_id_seq")+" TO "+runtime)
	}
	return statements
}

type postgresColumn struct {
	name, kind, def string
	notNull         bool
}

func verifyPostgresObjects(ctx context.Context, tx *sql.Tx, c PostgresContract) error {
	var safe bool
	if err := tx.QueryRowContext(ctx, `SELECT has_database_privilege($1,current_database(),'CONNECT') AND NOT has_database_privilege($1,current_database(),'CREATE') AND NOT has_database_privilege($1,current_database(),'TEMP') AND has_schema_privilege($1,'public','USAGE') AND NOT has_schema_privilege($1,'public','CREATE') AND (SELECT nspowner IN ((SELECT oid FROM pg_roles WHERE rolname=$2),(SELECT oid FROM pg_roles WHERE rolname='pg_database_owner')) FROM pg_namespace WHERE nspname='public')`, c.RuntimeRole, c.OwnerRole).Scan(&safe); err != nil || !safe {
		return postgresRefuse("runtime_privileges_refused")
	}
	for _, name := range []string{postgresStateTableName, postgresEnvelopeQueueTableName, postgresWritebackQueueTableName} {
		ref := "public." + name
		if err := tx.QueryRowContext(ctx, `SELECT c.relkind='r' AND c.relpersistence='p' AND NOT c.relispartition AND NOT EXISTS(SELECT 1 FROM pg_inherits h WHERE h.inhrelid=c.oid OR h.inhparent=c.oid) AND c.relowner=(SELECT oid FROM pg_roles WHERE rolname=$2) AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND NOT c.relhasrules AND NOT EXISTS(SELECT 1 FROM pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal) FROM pg_class c WHERE c.oid=to_regclass($1)`, ref, c.OwnerRole).Scan(&safe); err != nil || !safe {
			return postgresRefuse("table_shape_refused")
		}
		want := []postgresColumn{{"state_key", "text", "", true}, {"snapshot", "text", "", true}, {"updated_at", "timestamp with time zone", "now()", true}}
		if name != postgresStateTableName {
			want = []postgresColumn{{"id", "bigint", "nextval('" + name + "_id_seq'::regclass)", true}, {"queue_key", "text", "", true}, {"payload", "text", "", true}, {"created_at", "timestamp with time zone", "now()", true}}
		}
		rows, err := tx.QueryContext(ctx, `SELECT a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,COALESCE(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=to_regclass($1) AND a.attnum>0 ORDER BY a.attnum`, ref)
		if err != nil {
			return postgresRefuse("catalog_unavailable")
		}
		i := 0
		matches := true
		for rows.Next() {
			var got postgresColumn
			if rows.Scan(&got.name, &got.kind, &got.notNull, &got.def) != nil {
				matches = false
				break
			}
			if i >= len(want) || got != want[i] {
				matches = false
			}
			i++
		}
		if rows.Err() != nil {
			matches = false
		}
		rows.Close()
		if !matches || i != len(want) {
			return postgresRefuse("columns_refused")
		}
		pk := "state_key"
		expectedIndexes := 1
		if name != postgresStateTableName {
			pk = "id"
			expectedIndexes = 2
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*)=1 AND bool_and(contype='p' AND NOT condeferrable AND NOT condeferred AND convalidated AND conislocal AND conkey=ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid=to_regclass($1) AND attname=$2)]::smallint[]) FROM pg_constraint WHERE conrelid=to_regclass($1)`, ref, pk).Scan(&safe); err != nil || !safe {
			return postgresRefuse("constraints_refused")
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*)=$2 AND bool_and(i.indisvalid AND i.indisready AND i.indislive AND NOT i.indnullsnotdistinct AND i.indnatts=i.indnkeyatts AND i.indexprs IS NULL AND i.indpred IS NULL AND am.amname='btree' AND ((i.indisprimary AND i.indisunique AND ci.relname=$3 AND i.indoption::text='0' AND i.indkey::text=(SELECT attnum::text FROM pg_attribute WHERE attrelid=to_regclass($1) AND attname=$4)) OR (NOT i.indisprimary AND NOT i.indisunique AND ci.relname=$5 AND i.indoption::text='0 0' AND i.indkey::text='2 1')) AND ci.relowner=(SELECT oid FROM pg_roles WHERE rolname=$6)) FROM pg_index i JOIN pg_class ci ON ci.oid=i.indexrelid JOIN pg_am am ON am.oid=ci.relam WHERE i.indrelid=to_regclass($1)`, ref, expectedIndexes, name+"_pkey", pk, name+"_queue_key_id_idx", c.OwnerRole).Scan(&safe); err != nil || !safe {
			return postgresRefuse("indexes_refused")
		}
		if err = verifyPostgresTableGrants(ctx, tx, ref, name == postgresStateTableName, c); err != nil {
			return err
		}
		if name != postgresStateTableName {
			if err = verifyPostgresSequence(ctx, tx, name, c); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyPostgresTableGrants(ctx context.Context, tx *sql.Tx, ref string, state bool, c PostgresContract) error {
	for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"} {
		want := privilege == "SELECT" || privilege == "INSERT" || (state && privilege == "UPDATE") || (!state && privilege == "DELETE")
		var got bool
		if err := tx.QueryRowContext(ctx, `SELECT has_table_privilege($1,$2,$3)`, c.RuntimeRole, ref, privilege).Scan(&got); err != nil || got != want {
			return postgresRefuse("runtime_privileges_refused")
		}
	}
	var safe bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_class c CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) a WHERE c.oid=to_regclass($1) AND (a.grantee NOT IN ((SELECT oid FROM pg_roles WHERE rolname=$2),(SELECT oid FROM pg_roles WHERE rolname=$3)) OR (a.grantee=(SELECT oid FROM pg_roles WHERE rolname=$3) AND a.is_grantable)))`, ref, c.OwnerRole, c.RuntimeRole).Scan(&safe); err != nil || !safe {
		return postgresRefuse("object_acl_refused")
	}
	rows, err := tx.QueryContext(ctx, `SELECT attname,has_column_privilege($2,attrelid,attnum,'UPDATE'),COALESCE((SELECT bool_and(a.grantee=(SELECT oid FROM pg_roles WHERE rolname=$2) AND a.privilege_type='UPDATE' AND NOT a.is_grantable) FROM aclexplode(attacl) a),true) FROM pg_attribute WHERE attrelid=to_regclass($1) AND attnum>0 AND NOT attisdropped ORDER BY attnum`, ref, c.RuntimeRole)
	if err != nil {
		return postgresRefuse("catalog_unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var update, acl bool
		if rows.Scan(&name, &update, &acl) != nil || !acl || update != (state || name == "id") {
			return postgresRefuse("column_acl_refused")
		}
	}
	if rows.Err() != nil {
		return postgresRefuse("catalog_unavailable")
	}
	return nil
}

func verifyPostgresSequence(ctx context.Context, tx *sql.Tx, name string, c PostgresContract) error {
	var safe bool
	err := tx.QueryRowContext(ctx, `SELECT pg_get_serial_sequence($1,'id')=$2 AND s.seqtypid='bigint'::regtype AND s.seqstart=1 AND s.seqincrement=1 AND s.seqmin=1 AND s.seqmax=9223372036854775807 AND s.seqcache=1 AND NOT s.seqcycle AND c.relowner=(SELECT oid FROM pg_roles WHERE rolname=$3) AND has_sequence_privilege($4,c.oid,'USAGE') AND NOT has_sequence_privilege($4,c.oid,'SELECT') AND NOT has_sequence_privilege($4,c.oid,'UPDATE') AND EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.refobjid=to_regclass($1) AND d.refobjsubid=1 AND d.deptype='a') AND NOT EXISTS(SELECT 1 FROM aclexplode(COALESCE(c.relacl,acldefault('s',c.relowner))) a WHERE a.grantee NOT IN ((SELECT oid FROM pg_roles WHERE rolname=$3),(SELECT oid FROM pg_roles WHERE rolname=$4)) OR (a.grantee=(SELECT oid FROM pg_roles WHERE rolname=$4) AND a.is_grantable)) FROM pg_class c JOIN pg_sequence s ON s.seqrelid=c.oid WHERE c.oid=to_regclass($2)`, "public."+name, "public."+name+"_id_seq", c.OwnerRole, c.RuntimeRole).Scan(&safe)
	if err != nil || !safe {
		return postgresRefuse("sequence_refused")
	}
	return nil
}
