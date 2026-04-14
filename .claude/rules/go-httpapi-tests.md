---
paths:
  - "internal/httpapi/**/*_test.go"
---
# HTTP API Test Style

Write HTTP API tests as table-driven tests.

Call `t.Parallel()` on the parent test and on independent subtests, and keep expectations close to the table rows they validate.
