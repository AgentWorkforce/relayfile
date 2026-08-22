# Scoped cleanup record

Date: 2026-08-21
Result: **complete**

Only resources created for this benchmark were deleted. A post-delete Daytona
inventory query returned no matching sandbox IDs and no snapshot whose name
starts with `rf-five-agent-20260821-`.

Deleted sandbox IDs:

- `f64ab293-4a30-4856-bf07-bdcbed7efab7`
- `a749ae4c-d912-48c6-a442-6a1a78765a26`
- `abb6c8f9-09a3-4a3d-94c3-be9866b7c588`
- `925880a5-6028-491a-acaa-ae6469c4f098`
- `4549e42b-7878-48d4-b9e6-89b0a7620374`
- `c35ce175-ab1d-4236-91aa-f1a56b253032`

Deleted benchmark snapshot IDs:

- `c2d46b2d-b3ee-428d-8d8b-4722993e9e07`
- `fe7b556e-7fb5-494b-98c5-a77634c85d14`
- `6a8e3957-fa02-45ae-bf49-ff947864ed96`
- `d36521a8-2306-4039-bd7d-8cadde7bddbf`
- `37f52285-8097-4438-9f38-4f053f2a79eb`
- `0d98e1aa-b686-450f-b77d-30f1612fbfc7`
- `da2c6ad4-9dd2-4696-b812-e20b5096da14`

Local signed configuration files, the benchmark binary/archive build context,
the 152 MB seed-verification extraction, and generated Python bytecode were
also deleted. These disposable resources and Daytona sandboxes/snapshots are
not recoverable. Existing non-benchmark Daytona resources were not modified.
