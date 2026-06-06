# Deprecated Python pg_sandbox

This directory is an archival snapshot of the Python `pg_sandbox` tool
that lived at the repository root through May 2026. It is preserved
here for users mid-migration and for historical reference. It is no
longer maintained.

For the maintained tool, see the project [README](../README.md). The
full Python commit history is reachable via the `python-final` git tag.

## What's here

- `pg_sandbox` — the Python 3 CLI entry point.
- `pg_sandbox_help.py`, `pg_sandbox_errors.py` — help text and error
  definitions imported by the CLI.
- `build/build_executable.sh`, `build/build_single_file.sh` — the
  PyInstaller / single-file bundlers that produced a standalone
  `bin/pg_sandbox` binary on macOS and Linux.
- `README.md`, `DEMO.md`, `doc.md` — the Python tool's original
  documentation.
- `insert_simple_test_data.sql` — fixture referenced by the Python
  DEMO.

## Differences vs. the Go tool

The Go re-implementation has different env var names and a different
per-sandbox state file format. See the main README and `docs/` for
the canonical reference. There is no automatic migration of sandboxes
created by the Python tool — they need to be re-created with the Go
binary.
