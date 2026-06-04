// Tests for the Fake Runner.

package pgexec

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFakeCapturesCalls(t *testing.T) {
	f := &Fake{}
	ctx := context.Background()
	f.Run(ctx, "psql", "-c", "SELECT 1")
	f.Run(ctx, "pg_ctl", "stop", "-D", "/data")
	if got, want := len(f.Calls), 2; got != want {
		t.Fatalf("Calls len: got %d, want %d", got, want)
	}
	if f.Calls[0].Name != "psql" || f.Calls[0].Method != "Run" {
		t.Errorf("Calls[0]: %+v", f.Calls[0])
	}
	if !reflect.DeepEqual(f.Calls[0].Args, []string{"-c", "SELECT 1"}) {
		t.Errorf("Calls[0].Args: %v", f.Calls[0].Args)
	}
	if f.Calls[1].Name != "pg_ctl" {
		t.Errorf("Calls[1]: %+v", f.Calls[1])
	}
}

func TestFakeReturnsCannedResult(t *testing.T) {
	f := &Fake{}
	f.SetResult("psql", Result{Stdout: []byte("ok\n"), ExitCode: 0})
	res := f.Run(context.Background(), "psql", "-c", "SELECT 1")
	if string(res.Stdout) != "ok\n" {
		t.Errorf("Stdout: got %q, want %q", res.Stdout, "ok\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", res.ExitCode)
	}
}

func TestFakeReturnsZeroResultByDefault(t *testing.T) {
	f := &Fake{}
	res := f.Run(context.Background(), "psql")
	if res.ExitCode != 0 || len(res.Stdout) != 0 || res.Err != nil {
		t.Errorf("default Result not zero: %+v", res)
	}
}

func TestFakeRunWithStdinDrainsAndRecords(t *testing.T) {
	f := &Fake{}
	in := strings.NewReader("CREATE TABLE t(x int);\n")
	f.RunWithStdin(context.Background(), in, "psql", "-q")
	if len(f.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(f.Calls))
	}
	c := f.Calls[0]
	if c.Method != "RunWithStdin" {
		t.Errorf("Method: got %q, want RunWithStdin", c.Method)
	}
	if string(c.Stdin) != "CREATE TABLE t(x int);\n" {
		t.Errorf("Stdin: got %q, want CREATE TABLE t...", c.Stdin)
	}
}

func TestFakeLocateErr(t *testing.T) {
	f := &Fake{LocateErr: ErrFakeNoSuchBinary}
	if _, err := f.Locate("psql"); !errors.Is(err, ErrFakeNoSuchBinary) {
		t.Errorf("Locate: got %v, want ErrFakeNoSuchBinary", err)
	}
}

func TestFakeExecRecordsCall(t *testing.T) {
	f := &Fake{}
	err := f.Exec("psql", "-h", "127.0.0.1")
	if err != nil {
		t.Errorf("Exec on Fake: got %v, want nil", err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Method != "Exec" {
		t.Errorf("Calls: %+v", f.Calls)
	}
}

func TestFakeExecPropagatesLocateErr(t *testing.T) {
	f := &Fake{LocateErr: ErrFakeNoSuchBinary}
	err := f.Exec("psql")
	if !errors.Is(err, ErrFakeNoSuchBinary) {
		t.Errorf("Exec err: got %v, want ErrFakeNoSuchBinary", err)
	}
}

// Compile-time assertion that *Fake satisfies Runner.
var _ Runner = (*Fake)(nil)

// Compile-time assertion that *Exec satisfies Runner.
var _ Runner = (*Exec)(nil)
