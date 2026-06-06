// Tests for the free-port allocator.
//
// These tests are real-network tests by necessity: the only honest
// way to verify "this port is free" is to attempt to bind it. We
// scope the impact by binding on 127.0.0.1 only, and we let the
// kernel choose port 0 to occupy a known-busy port for the
// "skip-busy-port" test rather than picking a literal port that
// might already be busy on the developer's machine.

package portalloc

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestIsBusyOnFreePort(t *testing.T) {
	// Ask the kernel for an ephemeral port, then immediately close
	// it. That port is "probably free" — the kernel rarely reuses
	// it in the next few milliseconds — and gives us a number to
	// pass to IsBusy. We assert the predicate returns false.
	port := ephemeralPort(t)
	busy, err := IsBusy("127.0.0.1", port)
	if err != nil {
		t.Fatalf("IsBusy(127.0.0.1, %d): unexpected error: %v", port, err)
	}
	if busy {
		// Don't fail hard — it's possible (rare) that another
		// process grabbed the port between our ephemeral pick and
		// the IsBusy check. Just skip; the test is correct,
		// reality is racy.
		t.Skipf("port %d became busy between picking and checking; rerun", port)
	}
}

func TestIsBusyOnHeldPort(t *testing.T) {
	// Hold a port open ourselves, then assert IsBusy reports busy.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	busy, _ := IsBusy("127.0.0.1", port)
	if !busy {
		t.Errorf("IsBusy(127.0.0.1, %d) = false on a port we are actively listening on", port)
	}
}

func TestFreePortFindsAFreeOne(t *testing.T) {
	// Pick an ephemeral base far above the typical service range
	// so we're unlikely to hit a real service. 50000 is well
	// inside the dynamic/private range (49152..65535 per IANA).
	got, err := FreePort("127.0.0.1", 50000, 200)
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if got < 50000 || got >= 50200 {
		t.Errorf("FreePort returned %d, outside requested range [50000, 50200)", got)
	}
}

func TestFreePortSkipsBusyOne(t *testing.T) {
	// Hold a specific port, ask FreePort for a range starting at
	// that port, expect the returned port to be different.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	busyPort, _ := strconv.Atoi(portStr)

	got, err := FreePort("127.0.0.1", busyPort, 20)
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if got == busyPort {
		t.Errorf("FreePort returned the busy port %d", busyPort)
	}
}

func TestFreePortExhaustion(t *testing.T) {
	// Pick a single-port range pointed at a port we're holding
	// busy. FreePort must return an error mentioning exhaustion.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	busyPort, _ := strconv.Atoi(portStr)

	_, err = FreePort("127.0.0.1", busyPort, 1)
	if err == nil {
		t.Fatal("FreePort returned nil err on an exhausted range")
	}
	if !strings.Contains(err.Error(), "no free port") {
		t.Errorf("FreePort error doesn't mention 'no free port': %v", err)
	}
}

func TestFreePortBadArgs(t *testing.T) {
	if _, err := FreePort("127.0.0.1", 50000, 0); err == nil {
		t.Error("FreePort(scanRange=0): want error, got nil")
	}
	if _, err := FreePort("127.0.0.1", 0, 10); err == nil {
		t.Error("FreePort(base=0): want error, got nil")
	}
	if _, err := FreePort("127.0.0.1", 70000, 10); err == nil {
		t.Error("FreePort(base>65535): want error, got nil")
	}
}

// ephemeralPort asks the kernel for a free port and returns it,
// closing the temporary listener before returning. The port is
// nominally "free" but the test using it should tolerate a race
// where another process grabs it before the test checks.
func ephemeralPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ephemeralPort: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	port, _ := strconv.Atoi(portStr)
	return port
}
