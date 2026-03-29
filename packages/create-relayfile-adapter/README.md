# create-relayfile-adapter

`create-relayfile-adapter` scaffolds Relayfile integration packages for two starter kinds:

- `adapter`: webhook ingestion and file projection packages
- `provider`: connection proxy and webhook normalization packages

The package also ships static reference templates under `templates/` so you can inspect the expected starter layout without running the CLI.

## Usage

```bash
npx create-relayfile-adapter --name @acme/relayfile-github-adapter --provider github
```

```bash
npx create-relayfile-adapter \
  --name @acme/relayfile-linear-provider \
  --provider linear \
  --kind provider \
  --target-dir ./integrations/linear-provider
```

## CLI Options

- `--name`, `-n`: npm package name to generate
- `--provider`, `-p`: provider slug used in docs, config, and placeholder source
- `--kind`, `-k`: `adapter` or `provider` starter. Defaults to `adapter`
- `--target-dir`, `-d`: output directory. Defaults to the unscoped package name
- `--help`, `-h`: print help

## Template Placeholders

The files in `templates/` use these placeholder tokens:

- `{{packageName}}`: full npm package name
- `{{projectName}}`: human-readable project name
- `{{provider}}`: normalized provider slug such as `github` or `linear`

## Template Layout

```text
templates/
  adapter/
    .github/workflows/ci.yml
    .gitignore
    README.md
    package.json
    tsconfig.json
    src/
      config-schema.ts
      index.ts
    test/
      index.test.ts
    workflows/
      sample.ts
  provider/
    .github/workflows/ci.yml
    .gitignore
    README.md
    package.json
    tsconfig.json
    src/
      config-schema.ts
      index.ts
    test/
      index.test.ts
    workflows/
      sample.ts
```

## Generated Structure

Each generated package includes:

- package metadata with `build`, `test`, and `typecheck` scripts
- a starter implementation in `src/index.ts`
- a config schema placeholder in `src/config-schema.ts`
- a Vitest placeholder in `test/index.test.ts`
- a sample GitHub Actions workflow in `.github/workflows/ci.yml`
- a sample local workflow stub in `workflows/sample.ts`

## Notes

- The current CLI renders files programmatically from `src/index.ts`.
- The static `templates/` directory mirrors the intended starter structure and acts as a concrete reference fixture for future template-driven generation or tests.
