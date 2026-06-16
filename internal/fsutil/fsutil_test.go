package fsutil

import (
	"path/filepath"
	"testing"
)

func TestExpandTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// On Windows os.UserHomeDir reads USERPROFILE; set it too so the
	// table cases that expect expansion hold across platforms.
	t.Setenv("USERPROFILE", home)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/foo", filepath.Join(home, "foo")},
		{"tilde nested", "~/a/b/c", filepath.Join(home, "a", "b", "c")},
		{"other user untouched", "~bob/foo", "~bob/foo"},
		{"absolute untouched", "/etc/passwd", "/etc/passwd"},
		{"relative untouched", "rel/path", "rel/path"},
		{"empty untouched", "", ""},
		{"interior tilde untouched", "foo/~/bar", "foo/~/bar"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpandTilde(tc.in); got != tc.want {
				t.Errorf("ExpandTilde(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
