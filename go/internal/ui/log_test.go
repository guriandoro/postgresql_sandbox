// Tests for the leveled logger setup.

package ui

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLevelParsing(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantOK  bool
	}{
		{"debug", slog.LevelDebug, true},
		{"DEBUG", slog.LevelDebug, true},
		{" Debug ", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"", slog.LevelInfo, true}, // documented: empty = info
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"err", slog.LevelError, true},
		{"trace", slog.LevelInfo, false}, // unknown → not ok
		{"fatal", slog.LevelInfo, false},
	}
	for _, tc := range cases {
		got, ok := Level(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("Level(%q) = (%v, %v); want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelWarn)
	log.Debug("dropped at debug")
	log.Info("dropped at info")
	log.Warn("kept at warn", "k", "v")
	log.Error("kept at error", "k", "v")
	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("logger emitted below-threshold message: %q", out)
	}
	if !strings.Contains(out, "kept at warn") || !strings.Contains(out, "kept at error") {
		t.Errorf("logger swallowed an at-threshold message: %q", out)
	}
}

func TestLoggerSuppressesTime(t *testing.T) {
	// The text handler normally prefixes lines with `time=...`. We
	// replace the attribute with an empty Attr in NewLogger to
	// keep terminal output scannable. Verify the time prefix is
	// not present.
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo)
	log.Info("hello")
	if strings.Contains(buf.String(), "time=") {
		t.Errorf("logger emitted a time= attribute: %q", buf.String())
	}
}
