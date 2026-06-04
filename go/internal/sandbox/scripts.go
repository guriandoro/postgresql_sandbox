// Convenience-script generation for newly-deployed sandboxes.
//
// SPEC §4.5 requires deploy to drop a small set of executable POSIX
// shell scripts in the sandbox dir that wrap `pg_sandbox <cmd>
// --sandbox-dir <this-dir>`. The exact rendered text lives here so
// the contract is auditable in one place.
//
// We deliberately use a Go string constant (not go:embed) for the
// script body: the template is two lines, and a string constant
// keeps deploy.go free of an extra `embed.FS` import that would be
// noise relative to the saved bytes. If the script grows in a
// future SPEC revision, switching to embed is mechanical.
//
// Scripts emitted by this file are deliberately limited to the
// commands implemented in this slice (start/stop/restart/status).
// `use` and `run` are out of scope until §6.5/§6.6 land — emitting
// stubs that always fail would be more confusing than helpful.

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// scriptTemplate is the body of every convenience script. Two
// behaviors worth knowing:
//
//   - The binary path is BAKED IN from os.Executable() at deploy
//     time (the %s before the subcommand). Relying on PATH would
//     break catastrophically when a different `pg_sandbox` (e.g.
//     the legacy Python tool) is also installed: that other tool
//     would silently operate on this sandbox with different
//     semantics. The first sentinel arg below catches the case
//     where the user installs over the baked-in path.
//
//   - PG_SANDBOX_BIN env var, if set, takes priority over the baked-in
//     path. Lets power users move the binary or test a dev build
//     without redeploying their sandboxes.
//
//   - The `cd` + `dirname $0` dance resolves the sandbox dir from
//     the script's own location, so the script keeps working if the
//     sandbox dir is moved.
//
// SPEC §4.5 mandates POSIX sh; we use only POSIX features
// (`$(...)`, `exec`, `${VAR:-default}`, `"$@"` are all POSIX).
//
// Format args: %[1]s = absolute path to the deploying binary,
// %[2]s = subcommand name.
const scriptTemplate = `#!/bin/sh
exec "${PG_SANDBOX_BIN:-%[1]s}" %[2]s --sandbox-dir "$(cd "$(dirname "$0")" && pwd)" "$@"
`

// convenienceScripts lists the subcommand names to render as scripts
// in this slice. SPEC §4.5 also requires `use` and `run`; those are
// added when their commands land.
var convenienceScripts = []string{"start", "stop", "restart", "status"}

// writeConvenienceScripts renders each entry of convenienceScripts
// into sandboxDir/<name> with mode 0755. Existing files are
// overwritten — deploy is the only caller, and it has already
// established that the sandbox dir was empty.
//
// binPath is the absolute path to the pg_sandbox binary that's
// performing this deploy (typically os.Executable()). It's baked
// into each script so the scripts always invoke THIS tool, not
// whatever `pg_sandbox` happens to be on PATH (the legacy Python
// tool is a real risk on machines mid-migration).
func writeConvenienceScripts(sandboxDir, binPath string) error {
	if binPath == "" {
		return fmt.Errorf("sandbox: writeConvenienceScripts: empty binPath")
	}
	for _, name := range convenienceScripts {
		path := filepath.Join(sandboxDir, name)
		body := fmt.Sprintf(scriptTemplate, binPath, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return fmt.Errorf("sandbox: write %s: %w", path, err)
		}
		// os.WriteFile honors umask, which can mask 0755 down to 0644
		// on systems where the user's umask drops the exec bit (rare
		// but possible). Force the mode explicitly so the scripts are
		// always runnable.
		if err := os.Chmod(path, 0o755); err != nil {
			return fmt.Errorf("sandbox: chmod %s: %w", path, err)
		}
	}
	return nil
}
