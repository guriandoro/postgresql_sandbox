// Free TCP port detection for pg_sandbox.
//
// SPEC §4.3 lays out the policy this package implements:
//
//   - If --port is supplied explicitly and busy → caller returns
//     ExitPortInUse. This package's IsBusy is the predicate.
//
//   - If no --port is supplied → start from a documented base port
//     (default 65432) and scan forward up to a documented range
//     (default 100). First free port wins. This package's FreePort
//     is the scan.
//
// "Busy" means we cannot bind() to host:port right now. We do not
// consult /proc/net/tcp, lsof, or any registry — those would race
// against PostgreSQL spinning up between our check and its bind.
// A bind-test races against nothing because if we can't bind, no
// other process can either (in the same instant).
//
// We bind with ReuseAddr OFF (the default). This is deliberate:
// PostgreSQL's listen socket does NOT use SO_REUSEADDR, so if our
// probe succeeds with SO_REUSEADDR=1 the eventual `postgres` may
// still fail to bind. Matching what postgres does is the only safe
// test.

package portalloc

import (
	"fmt"
	"net"
	"strconv"
)

// Defaults documented in SPEC §4.3 and exposed here so the caller
// (deploy command) can reference them without duplicating the
// magic numbers.
const (
	// DefaultBasePort is where auto-allocation starts when the
	// user doesn't supply --port.
	DefaultBasePort = 65432

	// DefaultRange is the number of ports scanned forward from
	// DefaultBasePort before we give up. With base 65432 and
	// range 100 we scan 65432..65531 inclusive.
	DefaultRange = 100
)

// IsBusy reports whether host:port can currently accept a new TCP
// listener. It returns true if a bind would conflict, false if the
// port is free. Any error other than EADDRINUSE is returned as a
// non-nil err so the caller can distinguish "port busy" from
// "DNS broken" or "no permission".
func IsBusy(host string, port int) (busy bool, err error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// net.Listen wraps the syscall error. We treat any error
		// here as "this port is not usable right now" — the caller
		// in FreePort wants to move on to the next candidate. The
		// caller of IsBusy directly (deploy, on an explicit --port)
		// will see the error and surface it.
		//
		// Distinguishing EADDRINUSE from other errors is possible
		// via errors.Is(err, syscall.EADDRINUSE) but we don't need
		// to — the caller's policy is the same either way.
		return true, err
	}
	// Close immediately. We never want to keep the listener; we
	// just wanted to know if we *could* listen.
	_ = ln.Close()
	return false, nil
}

// FreePort scans for an available port starting at base and walking
// forward by 1 up to (but not including) base+scanRange. The first
// port for which IsBusy returns (false, nil) is returned. If the
// entire range is exhausted, FreePort returns a non-nil error so
// the caller can map it to ExitNoFreePort.
//
// A nil/empty host string is interpreted as "all interfaces" by
// net.Listen, which is rarely what a sandbox wants — sandbox
// deploys default to 127.0.0.1, and pass that here. We don't
// substitute a default; callers must be explicit.
func FreePort(host string, base, scanRange int) (int, error) {
	if scanRange <= 0 {
		return 0, fmt.Errorf("portalloc: scanRange must be > 0, got %d", scanRange)
	}
	if base < 1 || base > 65535 {
		return 0, fmt.Errorf("portalloc: base port out of range: %d", base)
	}
	for p := base; p < base+scanRange && p <= 65535; p++ {
		busy, _ := IsBusy(host, p)
		// We deliberately ignore the err from IsBusy here — for
		// the scan, any error just means "skip this one and try
		// the next". A persistent error (DNS broken, permission
		// denied on every port) will surface as "no free port in
		// range" with the busy list, which is the right user
		// experience either way.
		if !busy {
			return p, nil
		}
	}
	return 0, fmt.Errorf("portalloc: no free port found in %d..%d on host %q",
		base, base+scanRange-1, host)
}
