# Benchmark cleanup

Date: 2026-08-22

- E2B service sandbox `ih8xcaiuoon8u1w6ittrw` and agent sandbox
  `imhb24eqytdtb3ova0q5y` reached their configured TTL after all final raw
  evidence was copied locally.
- Dedicated Daytona core sandbox
  `424f8877-6bd0-49de-8565-5c6a9592954d` was explicitly deleted.
- Core-only processes, artifacts, and token state were removed from the reused
  Daytona benchmark agent `81d4a121-40db-43b5-b474-5be0784d4898`.
- The reused Daytona agent itself was not deleted because it predated this
  benchmark and remains an owned launch-demo resource.

No unrelated E2B or Daytona resource was modified or deleted.
