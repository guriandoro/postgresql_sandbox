#!/usr/bin/env bash
#
# build_executable.sh — bundle pg_sandbox into a single native executable with PyInstaller.
#
# WHAT THIS PRODUCES
# ------------------
# A standalone binary at ../bin/pg_sandbox that embeds CPython and the three Python sources:
#   pg_sandbox (entry script), pg_sandbox_help.py, pg_sandbox_errors.py
#
# PostgreSQL binaries are NOT included; they remain external (PGS_BIN_DIR / -b), by design.
#
# SUPPORTED PLATFORMS
# -------------------
# This script only supports building on **macOS (Darwin)** and **Linux**.
# PyInstaller does not produce one portable binary for all Unix systems — you must run this
# script on each OS (and typically each CPU architecture, e.g. arm64 vs x86_64) you ship for.
# Windows is intentionally unsupported here (exit with a clear message).
#
# PREREQUISITES
# -------------
# - Python 3 on PATH as `python3` (must provide the `venv` module)
# - PyInstaller either:
#     - installed for that interpreter (python3 -m pip install pyinstaller), or
#     - absent: this script creates build/.venv/, installs PyInstaller inside it, and uses that
#       interpreter for the bundle (helps on PEP 668 / Homebrew Python where global pip is blocked)
#
# USAGE
# -----
# From the repository root:
#       ./build/build_executable.sh
#
# Or from anywhere:
#       /path/to/repo/build/build_executable.sh
#
# CI / SANDBOX NOTES
# ------------------
# PyInstaller may write under the user home directory (e.g. binary processing caches). Restricted
# sandboxes that disallow writes outside the repo must either allow those paths or set vendor
# tooling to permit PyInstaller’s cache.
#

set -euo pipefail

# Resolve repository root: this file lives in <repo>/build/, so parent directory is the root.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${REPO_ROOT}"

# --- Platform gate -----------------------------------------------------------
# PyInstaller could theoretically be invoked on Windows, but this project only documents and
# tests macOS/Linux bundles from this script. Failing fast avoids confusing partial setups.
OS_KERNEL="$(uname -s)"
case "${OS_KERNEL}" in
Darwin | Linux) ;;
*)
  echo "build_executable.sh: unsupported operating system '${OS_KERNEL}'." >&2
  echo "This script only supports macOS (Darwin) and Linux." >&2
  exit 1
  ;;
esac

# --- Python + PyInstaller -----------------------------------------------------
# Prefer system/interpreter-wide PyInstaller when present; otherwise bootstrap a repo-local venv
# under build/.venv so PEP 668 managed Pythons (e.g. Homebrew) do not require --break-system-packages.
PYTHON_RUNNER="python3"
if ! python3 -m PyInstaller --version >/dev/null 2>&1; then
  VENV="${SCRIPT_DIR}/.venv"
  if [[ ! -x "${VENV}/bin/python" ]]; then
    echo "PyInstaller not found for python3; creating venv at ${VENV}"
    python3 -m venv "${VENV}"
    "${VENV}/bin/pip" install --upgrade pip
    "${VENV}/bin/pip" install pyinstaller
  fi
  PYTHON_RUNNER="${VENV}/bin/python"
fi
if ! "${PYTHON_RUNNER}" -m PyInstaller --version >/dev/null 2>&1; then
  echo "build_executable.sh: PyInstaller is still not available after venv bootstrap." >&2
  exit 1
fi

# --- Output locations --------------------------------------------------------
# distpath: final executable lands here (committed script assumes ../bin/pg_sandbox).
BIN_DIR="${REPO_ROOT}/bin"
# workpath: PyInstaller's build cache (extracted libs, COLLECT work for onedir mode, etc.).
# Safe to delete between builds; kept under build/ so the repo root stays tidy.
WORK_DIR="${REPO_ROOT}/build/pyinstaller-work"
# specpath: PyInstaller may write pg_sandbox.spec here on first run for reproducibility/editing.
SPEC_DIR="${REPO_ROOT}/build"

mkdir -p "${BIN_DIR}" "${WORK_DIR}" "${SPEC_DIR}"

ENTRY_SCRIPT="${REPO_ROOT}/pg_sandbox"
if [[ ! -f "${ENTRY_SCRIPT}" ]]; then
  echo "build_executable.sh: missing entry script at ${ENTRY_SCRIPT}" >&2
  exit 1
fi

echo "Building standalone pg_sandbox → ${BIN_DIR}/pg_sandbox"
echo "  OS:     ${OS_KERNEL}"
echo "  Python: $("${PYTHON_RUNNER}" --version 2>&1)"

# --- PyInstaller invocation ---------------------------------------------------
# --onefile      Single executable (bootstrap unpacks to a temp dir at runtime).
# --name         Base name of the output binary (pg_sandbox).
# --distpath     Where the final executable is written (bin/).
# --workpath     Scratch/build tree (isolated under build/).
# --specpath     Where generated .spec files go if PyInstaller creates them.
#
# The entry script must be the path to the repository's `pg_sandbox` file (no .py extension).
# Static imports of pg_sandbox_help and pg_sandbox_errors are traversed automatically; if the
# analyzer ever misses a module, add e.g. --hidden-import pg_sandbox_help.
"${PYTHON_RUNNER}" -m PyInstaller \
  --onefile \
  --name pg_sandbox \
  --distpath "${BIN_DIR}" \
  --workpath "${WORK_DIR}" \
  --specpath "${SPEC_DIR}" \
  "${ENTRY_SCRIPT}"

echo "Done. Run:  ${BIN_DIR}/pg_sandbox --help"
