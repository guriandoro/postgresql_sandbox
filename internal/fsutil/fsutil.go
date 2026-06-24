// Package fsutil holds small filesystem-path helpers shared across the
// CLI and the internal packages.
package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandTilde expands a leading "~" or "~/" in p to the current user's
// home directory. "~otheruser" forms and paths without a leading "~"
// are returned unchanged. If the home directory can't be determined it
// returns p unchanged — best-effort, matching the existing "swallow
// errors and leave the path as-is" precedent around filepath.Abs.
//
// This must run before any filepath.IsAbs/Abs/os.Stat handling: Go's
// stdlib treats "~" as an ordinary filename byte, so filepath.IsAbs
// ("~/foo") is false and filepath.Abs would otherwise turn it into a
// literal "~" directory under the current working directory.
func ExpandTilde(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		// filepath.Join(home, "") correctly yields home for a bare "~".
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return p // ~otheruser — unsupported, leave as-is
}
