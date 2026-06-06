// Package portalloc finds a free TCP port on a given host, walking a
// configurable range from a base port. Used by deploy when --port is
// not supplied explicitly.
//
// See SPEC.md §4.3 for the allocation policy this package implements.
package portalloc
