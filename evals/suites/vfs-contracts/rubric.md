# VFS Contracts Rubric

VFS contract cases are deterministic. A passing run must show that ordinary
filesystem operations produce the same observable state an agent would see from
the mount: reads return current bytes, writes change later reads, creates appear
in directory listings, and deletes disappear from both snapshots and listings.
