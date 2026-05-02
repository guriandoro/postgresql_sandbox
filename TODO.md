# PostgreSQL Sandbox - TODO List (regenerated from code)

This document is auto-generated from `TODO`/`FIXME` comments currently present in
the source code. It tracks pending tasks and improvements for the PostgreSQL
Sandbox project.

## High Priority (Critical Issues)

### Argument Validation
- **File**: `pg_sandbox` (lines 124-125, in `parse_opts`)
- **Issue**: No argument validation implemented after `getopt` parsing
- **Tasks**:
  - Validate all command-line arguments
  - (low priority) Strip unneeded `.` / `..` path components from path arguments
- **Impact**: High - affects every command and overall robustness

### Error Handling Improvements (`pgserr` error codes)
- **File**: `pg_sandbox` (lines 250, 251, 267, 282, 299, 314, in `exec_build`)
- **Issue**: Subprocess failures during build are returned as raw `-1` instead
  of proper `pgserr` error codes
- **Tasks**:
  - Replace `return -1` with proper `pgserr` error codes after each failed
    `./configure`, `make`, `make install`, `make` (contrib) and
    `make install` (contrib) step
  - Add helper output telling the user where to look (e.g. `config.log`) when
    `./configure` fails (line 251)
- **Impact**: High - affects debugging and user experience during builds

### Critical Bug Fixes in `exec_run`
- **File**: `pg_sandbox` (lines 525-526)
- **Issues**:
  - No validation when `argv` is empty - need to error out cleanly
  - No handling for the case where binaries no longer exist under
    `/opt/postgres/xx/` - need `try`/`except` around all `get_binary_path`
    calls across the codebase
- **Impact**: High - can cause crashes / unhelpful tracebacks

## Medium Priority (Important Improvements)

### Build System Enhancements
- **File**: `pg_sandbox` (lines 226-227, 259, in `exec_build`)
- **Issues**:
  - `--without-icu` is hardcoded in the `./configure` command
  - `--with-openssl` was previously hardcoded; it's now removed/commented out
    because it's not portable across all build environments
  - No way to pass extra parameterized `./configure` options
  - `make -j8` is hardcoded instead of using all available cores
- **Tasks**:
  - Add a parameter to enable/disable ICU
  - Add `--with-openssl` as an optional CLI argument to the `build` command
    (off by default), so users that have OpenSSL headers available can opt in
    without editing the source
  - Allow user-supplied extra `./configure` options (generic pass-through)
  - Use `os.cpu_count()` (or similar) instead of `-j8`. Don't use all available, round to about half
- **Impact**: Medium - affects build flexibility and performance

### Port Management
- **File**: `pg_sandbox` (line 335, in `exec_deploy`)
- **Issue**: When the chosen port is in use, the deploy fails outright
- **Task**: If the user did not pass `--port` (i.e. `pgs_port` is the default),
  automatically try other ports before erroring out
- **Impact**: Medium - improves user experience

### Logging and Debugging of Subprocesses
- **File**: `pg_sandbox` (line 326, end of `exec_build`)
- **Issue**: Subprocess stdout/stderr is not persisted to disk
- **Task**: Write stdout/stderr of each subprocess to log files for
  troubleshooting failed builds
- **Impact**: Medium - affects debugging capabilities

### Report Command File Naming
- **File**: `pg_sandbox` (lines 510-511, in `exec_report`)
- **Issue**: To support batch reports, the resulting HTML file is currently
  named by prepending the `pg_gather` filename. This is a stop-gap; needs
  validation / a better strategy.
- **Task**: Evaluate the current `<pg_gather>_GatherReport.html` naming and
  decide whether a better scheme is needed for batch runs
- **Impact**: Medium - affects report organization

## Low Priority (Nice to Have)

### Help System Completion
- **File**: `pg_sandbox_help.py` (lines 33-62, in `print_help`)
- **Issue**: All per-command help branches just `print("TODO")`
- **Tasks**: Implement actual help content for each command:
  - `build`
  - `deploy`
  - `destroy`
  - `report`
  - `restart`
  - `run`
  - `setenv`
  - `start`
  - `stop`
  - `use`
- **Impact**: Low - affects documentation, not functionality

## Summary of Code TODOs Counted

- `pg_sandbox`: 17 `TODO` comments
  - Lines: 124, 125, 226, 227, 250, 251, 259, 267, 282, 299, 314, 326, 335, 510, 511, 525, 526
- `pg_sandbox_help.py`: 10 `TODO` comments
  - Lines: 34, 37, 40, 43, 46, 49, 52, 55, 58, 61
- **Total**: 27 `TODO` comments across 2 files

## Development Guidelines

### Priority Order
1. **High Priority** - core functionality / crash-prone paths
2. **Medium Priority** - UX and build flexibility
3. **Low Priority** - documentation polish

### Recurring Pattern
Many TODOs are variants of "handle gracefully with proper error code from
`pgserr`". A small helper in `pg_sandbox_errors.py` that wraps the
"print stderr + return appropriate code" pattern would let us close ~5
TODOs at once in `exec_build`.

---

**Last Updated**: regenerated from code on 2026-05-01
