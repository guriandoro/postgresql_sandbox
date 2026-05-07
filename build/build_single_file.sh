#!/usr/bin/env bash
#
# build_single_file.sh — merge pg_sandbox, pg_sandbox_errors.py, and
# pg_sandbox_help.py into a single self-contained Python script.
#
# WHAT THIS PRODUCES
# ------------------
# A standalone Python script at ../bin/pg_sandbox that contains all
# error constants/functions and help text inlined. No PyInstaller, no
# extraction step, instant startup — only requires python3 on the host.
#
# USAGE
# -----
# From the repository root:
#       ./build/build_single_file.sh
#
# Or from anywhere:
#       /path/to/repo/build/build_single_file.sh
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SRC="${REPO_ROOT}/pg_sandbox"
ERRORS="${REPO_ROOT}/pg_sandbox_errors.py"
HELP="${REPO_ROOT}/pg_sandbox_help.py"
OUT="${REPO_ROOT}/bin/pg_sandbox"

for f in "${SRC}" "${ERRORS}" "${HELP}"; do
  if [[ ! -f "${f}" ]]; then
    echo "build_single_file.sh: missing source file: ${f}" >&2
    exit 1
  fi
done

mkdir -p "$(dirname "${OUT}")"

TMP="${OUT}.tmp.$$"
trap 'rm -f "${TMP}"' EXIT

# 1. Emit shebang + stdlib imports from the main script (lines before the
#    pg_sandbox_errors / pg_sandbox_help imports).  We also skip the
#    "import pg_sandbox_errors" and "import pg_sandbox_help" lines and the
#    bare "import sys" / "import os" that the helper modules repeat.
head -1 "${SRC}" > "${TMP}"
echo "" >> "${TMP}"

# Collect stdlib imports from pg_sandbox (skip the two local imports).
sed -n '2,/^from subprocess/p' "${SRC}" \
  | grep -v '^import pg_sandbox_errors' \
  | grep -v '^import pg_sandbox_help' \
  >> "${TMP}"

# 2. Inline pg_sandbox_errors.py (skip its own stdlib imports — already present).
{
  echo ""
  echo "# --- inlined from pg_sandbox_errors.py ---"
  grep -v '^import sys$' "${ERRORS}" \
    | grep -v '^import os$' \
    | grep -v '^$' | cat   # strip blank lines at boundaries
  echo ""
  echo "# --- end pg_sandbox_errors.py ---"
} >> "${TMP}"

# 3. Inline pg_sandbox_help.py (no imports to strip).
{
  echo ""
  echo "# --- inlined from pg_sandbox_help.py ---"
  cat "${HELP}"
  echo ""
  echo "# --- end pg_sandbox_help.py ---"
} >> "${TMP}"

# 4. Append the rest of the main script (everything after the
#    "from subprocess import PIPE, run" line), replacing the module
#    prefixes pgserr. and pgshlp. with direct references.
tail -n +18 "${SRC}" \
  | sed 's/pgserr\.//g; s/pgshlp\.//g' \
  >> "${TMP}"

mv "${TMP}" "${OUT}"
chmod +x "${OUT}"

echo "Built single-file script → ${OUT}"
echo "  Size: $(wc -c < "${OUT}" | tr -d ' ') bytes"
echo "  Run:  ${OUT} --help"
