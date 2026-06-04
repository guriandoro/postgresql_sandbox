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

// scriptTemplate is the body of every convenience script. The `cd`
// + `dirname $0` dance resolves the sandbox dir from the script's
// own location, so the script keeps working if the sandbox dir is
// moved (a common power-user workflow). %s is the subcommand name.
//
// SPEC §4.5 mandates POSIX sh, not bash; we use only POSIX features
// (`$(...)` is POSIX since 2001, `exec` is POSIX, `"$@"` is POSIX).
const scriptTemplate = `#!/bin/sh
exec pg_sandbox %s --sandbox-dir "$(cd "$(dirname "$0")" && pwd)" "$@"
`

// convenienceScripts lists the subcommand names to render as scripts
// in this slice. SPEC §4.5 also requires `use` and `run`; those are
// added when their commands land.
var convenienceScripts = []string{"start", "stop", "restart", "status"}

// writeConvenienceScripts renders each entry of convenienceScripts
// into sandboxDir/<name> with mode 0755. Existing files are
// overwritten — deploy is the only caller, and it has already
// established that the sandbox dir was empty.
func writeConvenienceScripts(sandboxDir string) error {
	for _, name := range convenienceScripts {
		path := filepath.Join(sandboxDir, name)
		body := fmt.Sprintf(scriptTemplate, name)
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
