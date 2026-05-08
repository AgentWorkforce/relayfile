# Writeback Rubric

Writeback cases focus on observable file-native adapter semantics: canonical
files map to PATCH, draft filenames map to CREATE plus a receipt, and schema or
read-only validation failures must be visible without mutating the source file.
