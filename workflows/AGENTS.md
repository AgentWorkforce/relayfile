# Workflows

Write workflow definitions in the established `@agent-relay/sdk/workflows` style.

- Start each file with a header comment that states purpose and the `agent-relay run workflows/<file>.ts` entrypoint.
- Build workflows with `workflow(...)`, then set `.description()`, `.pattern()`, `.channel()`, concurrency, and timeout before agents and steps.
- Use deterministic steps with `captureOutput: true` when later steps interpolate that output.
- Keep agent definitions explicit about CLI, preset, role, and working directory.
- End files with `main().catch(...)` so failures set a non-zero exit code.
