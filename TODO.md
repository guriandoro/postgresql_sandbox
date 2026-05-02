# PostgreSQL Sandbox - TODO List (regenerated from code)

This document is auto-generated from `TODO`/`FIXME` comments currently present
in the source code. It tracks pending tasks and improvements for the
PostgreSQL Sandbox project.

## Status

All previously tracked in-code `TODO` / `FIXME` comments have been resolved.
A scan of `pg_sandbox` and `pg_sandbox_help.py` returns no matches.

The items that were closed in the latest pass include:

### High priority (closed)
- Argument validation in `parse_opts` (port range, non-empty strings, NUL
  rejection, path normalization).
- Proper `pgserr` exit codes for every failed `./configure` / `make` /
  `make install` / `make (contrib)` / `make install (contrib)` step in
  `exec_build`, plus a `config.log` hint on `./configure` failure.
- `exec_run` now validates that `argv` is non-empty before any side effect,
  and `get_binary_path` / `get_latest_binary_from_default_path` exit cleanly
  with `ERR_BIN_NOT_FOUND` when the binary directory or binary itself is
  missing (instead of returning an integer disguised as a path).

### Medium priority (closed)
- `build` command is now flexible:
  - `--with-icu` opt-in (still defaults to `--without-icu`).
  - `--with-openssl` opt-in (replaces the previously commented-out hardcode).
  - `--configure-opts="..."` for arbitrary extra `./configure` flags.
  - `make` parallelism derived from `os.cpu_count()` (about half the cores,
    rounded up, min 1) instead of a hardcoded `-j8`.
- `exec_deploy` auto-falls-back to alternate ports (next 100) when the
  default port is in use *and* the user did not explicitly pass `--port`.
- `exec_build` persists stdout/stderr of every subprocess step to
  `<PGS_BUILD_DIR>/logs/<version>/<step>.{stdout,stderr}.log` and surfaces
  the path in failure hints.
- `exec_report` now names the HTML output `<basename>.GatherReport.html`
  (derived from the input file's basename without extension) instead of the
  earlier `<full_arg>_GatherReport.html` stop-gap.

### Low priority (closed)
- `pg_sandbox_help.py` has actual per-command help for every command
  (build, deploy, destroy, report, restart, run, setenv, start, status,
  stop, use). `pg_sandbox help <cmd>` and `pg_sandbox <cmd> --help` both
  print the per-command text.

## Possible Future Work (not currently tracked as code TODOs)

These are not blocking and are not represented as `TODO` comments in the
source today, but came up while closing the items above:

- **Pre-flight for `exec_build` external steps** — `curl` / `tar` failures
  currently let the script continue and crash later with a stack trace
  when the expected source dir is missing. A clean `pgserr.print_error_and_exit`
  on non-zero return codes from `curl` and `tar` would mirror the rest of
  `exec_build`.
- **Subprocess log streaming** — today we capture stdout/stderr fully and
  write them at the end of each step. For very long builds, streaming to
  the log file in real time (so the user can `tail -f` it) would be nicer.
- **Shared subprocess wrapper** — `exec_deploy`, `exec_start`, `exec_stop`
  and friends still use the same `run(..., stdout=PIPE, stderr=PIPE, ...)`
  + manual returncode check pattern. Folding these into a single helper
  (similar to `print_step_failure_and_exit` / `_persist_subprocess_log`)
  would cut a fair amount of repetition.
- **`status` short-circuit** — the general help table now lists the
  `status` command, but its dedicated entry in the README/DEMO docs could
  use a refresh.

## Summary of Code TODOs Counted

- `pg_sandbox`: 0 `TODO` comments
- `pg_sandbox_help.py`: 0 `TODO` comments
- **Total**: 0 `TODO` comments across 2 files

## Development Guidelines

### Priority Order
1. **High Priority** - core functionality / crash-prone paths
2. **Medium Priority** - UX and build flexibility
3. **Low Priority** - documentation polish

### Recurring Pattern
The "print stderr + exit with appropriate `pgserr` code" pattern is now
centralized in `pgserr.print_step_failure_and_exit`, and per-step subprocess
logs go through `_persist_subprocess_log` in `pg_sandbox`. Future
multi-step subprocess routines should reuse both rather than re-inlining
the same shape.

---

**Last Updated**: regenerated from code on 2026-05-01
