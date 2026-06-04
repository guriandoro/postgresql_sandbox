// Package cluster orchestrates groups of related sandboxes and owns
// the cluster manifest format. A cluster is one primary/publisher
// plus N standbys/subscribers managed as a unit.
//
// See SPEC.md §6.11 for the contract this package implements.
package cluster
