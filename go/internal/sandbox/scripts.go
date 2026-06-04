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
// The script set covers every SPEC §4.5 required command:
// start, stop, restart, status, use, run. They share one template
// because each one boils down to "exec the real binary with this
// subcommand, --sandbox-dir pointed at me, and $@ appended" — the
// extra args slot is what makes ./run pg_dump and ./use -c "SELECT 1"
// both work without per-script branching.

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// scriptTemplate is the body of every convenience script. Three
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
// Format args:
//
//	%[1]s = absolute path to the deploying binary
//	%[2]s = subcommand name
//	%[3]s = literal "-- " (with trailing space) for commands that
//	        forward user args (use, run); empty otherwise. The `--`
//	        stops pg_sandbox's own flag parser so flags meant for
//	        the inner binary (e.g. `./use -c "SELECT 1;"`) aren't
//	        eaten by our flag set as unknown options.
//
// SPEC §4.5 mandates POSIX sh; we use only POSIX features
// (`$(...)`, `exec`, `${VAR:-default}`, `"$@"`, `--` are all POSIX).
const scriptTemplate = `#!/bin/sh
exec "${PG_SANDBOX_BIN:-%[1]s}" %[2]s --sandbox-dir "$(cd "$(dirname "$0")" && pwd)" %[3]s"$@"
`

// convenienceScripts lists the subcommand names rendered as scripts
// in the sandbox dir. The set matches SPEC §4.5 exactly: every
// command a user is expected to run "from inside" a sandbox has a
// matching script. The forwardsUserArgs flag controls whether we
// emit a literal `--` between our injected flags and "$@" — needed
// for use/run because their forwarded args may begin with `-` and
// would otherwise be misparsed as pg_sandbox's own flags.
var convenienceScripts = []struct {
	name             string
	forwardsUserArgs bool
}{
	{"start", false},
	{"stop", false},
	{"restart", false},
	{"status", false},
	{"use", true},
	{"run", true},
	// `promote` is included so a standby author can run
	// `./promote` from inside the sandbox dir. The script is
	// harmless on a primary (the command itself refuses with
	// ExitNotAStandby) so we always emit it.
	{"promote", false},
}

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
	for _, s := range convenienceScripts {
		path := filepath.Join(sandboxDir, s.name)
		sep := ""
		if s.forwardsUserArgs {
			sep = "-- "
		}
		body := fmt.Sprintf(scriptTemplate, binPath, s.name, sep)
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
