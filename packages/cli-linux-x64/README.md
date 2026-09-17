# @relayfile/cli-linux-x64

Prebuilt `relayfile-cli` binary (`bin/relayfile-cli`) for **Linux (x64)**.

This package is installed automatically as an optional dependency of
[`@relayfile/sdk`](https://www.npmjs.com/package/@relayfile/sdk). You do not
need to depend on it directly. The SDK resolves the correct platform binary at
runtime via `require.resolve` (see `@relayfile/sdk/relay-cli`), which is how
both `relayfile <cmd>` and `agent-relay file <cmd>` find the CLI.

It carries no install script and downloads nothing: npm installs only the
package matching the host's `os`/`cpu`, so the binary arrives with registry
integrity metadata and the install works offline and in CI.

See the [relayfile repository](https://github.com/AgentWorkforce/relayfile)
for source and build tooling.
