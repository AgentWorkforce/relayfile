# PostgreSQL owner bootstrap and verified runtime

The existing server and PostgreSQL constructors keep their historical lazy
schema initialization. Select the explicit `postgres serve` command to use a
separate runtime role that performs no schema initialization. This command
checks the schema, effective privileges and TLS connection, then loads the
persisted state before starting queue workers or the HTTP listener.

The verified contract targets PostgreSQL17 and later, including its MAINTAIN
privilege checks. Use one database dedicated to this service. Provision its owner as a restricted
NOLOGIN role and provision a separate runtime role with NOINHERIT, NOSUPERUSER,
NOBYPASSRLS, NOCREATEROLE, NOCREATEDB and NOREPLICATION. These commands do not
create roles, set passwords or transfer database ownership.

The bootstrap operator must be able to `SET ROLE` to the existing database
owner. Pass its complete credential URL on stdin to the official command:

```sh
relayfile postgres bootstrap --database relayfile --owner-role relayfile \
  --runtime-role relayfile_runtime --operator-role postgres \
  --ca-sha256 "$PUBLIC_CA_SHA256" --dsn-stdin
```

Bootstrap creates the three existing Relayfile tables and their queue indexes
and sequences in an empty `public` schema, under the NOLOGIN owner. It grants
only the runtime privileges used by the state and queue implementations. Queue
row locking requires column-level `UPDATE(id)` in addition to SELECT, INSERT
and DELETE. Database CREATE/TEMP and schema CREATE are refused for the runtime
identity, including privileges inherited from PUBLIC. Existing incompatible or
foreign-owned objects are refused; repeat bootstrap verifies them rather than
repairing them or deleting data.

Verify using the runtime credential on stdin:

```sh
relayfile postgres verify --database relayfile --owner-role relayfile \
  --runtime-role relayfile_runtime --ca-sha256 "$PUBLIC_CA_SHA256" --dsn-stdin
```

Serve with the runtime URL in a projected Kubernetes Secret file. The argument
is a file path, never a credential value:

```sh
relayfile postgres serve --database relayfile --owner-role relayfile \
  --runtime-role relayfile_runtime --ca-sha256 "$PUBLIC_CA_SHA256" \
  --dsn-file /var/run/relayfile-postgres/database-url
```

`serve` also accepts `--dsn-stdin`; `bootstrap` and `verify` accept stdin only.
The operator credential must never be mounted in the serving Pod. The ordinary
HMAC and bearer-auth settings still apply to `serve`.

All URLs must explicitly contain host, port, database, username and password,
with `sslmode=verify-full` and an absolute `sslrootcert` path. The public CA's
bytes must match the supplied SHA256. Only an optional bounded connect timeout
is accepted as another connection option. Ambient PostgreSQL connection
variables and independent legacy storage overrides are refused in this mode.
All three backends share the verified database connection and use qualified
`public` relations.

The existing `/health` endpoint remains HTTP liveness. This change provides
startup admission, not a continuous database-readiness endpoint or a backup
and migration system. Provider authorization, mount access and application
read/write acceptance still need their normal separate checks. Deployment
qualification must exercise the real PostgreSQL version and strict TLS with
the selected native binary; unit contract tests do not establish that proof.

## Validation scope

The changed server and storage packages pass `go test ./cmd/relayfile
./internal/relayfile` and `go vet ./cmd/relayfile ./internal/relayfile`. Focused
race tests cover credential admission, read-only refusal paths, queue snapshot
errors and checked startup. A static Linux arm64 server was also exercised in
an isolated PostgreSQL17.11 fixture with a real private CA and SCRAM passwords.
Its 14 checks covered official bootstrap/repeat, runtime verification, wrong
CA/hostname/password refusals, runtime privilege restrictions, file persistence
through a server restart, and refusal of damaged state, queues, grants and
ownership before serving.

That fixture used its own synthetic introspection responder. It establishes
the database and startup behavior, including the file HTTP roundtrip; it does
not establish Control integration, production Go queue FIFO behavior or a
qualified final container image.

The full local repository suite also encounters three unchanged baseline
failures: `TestMountDaemonCommandMatchesAliasesAndLocalDirBoundaries`,
`TestBootstrapProgressTouchesStateFileMidPage`, and
`TestBootstrapProgressCreatesStateFileBeforeSlowFirstPage`. Those failures were
reproduced using the unchanged mount CLI and synchronization sources. They are
separate from this PostgreSQL slice.
