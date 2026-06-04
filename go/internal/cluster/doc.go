// Package cluster orchestrates groups of related sandboxes and owns
// the cluster manifest format. A cluster is one primary/publisher
// plus N standbys/subscribers managed as a unit.
//
// See SPEC.md §6.11 for the contract this package implements.
//
// Design notes for the cluster slice:
//
//   - The cluster package is a thin orchestrator over the sandbox
//     package: Deploy fans out to sandbox.Deploy N+1 times (member 0
//     standalone, members 1..N as standbys or subscribers); Destroy
//     fans out to sandbox.Destroy in reverse order; Status fans out
//     to sandbox.Status. The cluster package never reimplements the
//     per-member operations — it composes them.
//
//   - Member deploys are sequential per SPEC §11 open question 3's
//     documented default. Parallelism is a future tradeoff if the
//     wall-clock matters for large N.
//
//   - On partial deploy failure (member N fails after 0..N-1 deployed)
//     we leave the partial state on disk for inspection, write the
//     manifest with the members that did make it, and return
//     ExitClusterDeployFailed. No auto-rollback — SPEC §6.11 framing
//     plus the implementation brief both say "leave for inspection".
//
//   - --sync-count is parsed but the synchronous_standby_names wiring
//     is deferred to a follow-up slice. We log a warn-level diagnostic
//     when sync-count > 0 and proceed with all members async. The
//     flag plumbing lives here so a later slice doesn't need to touch
//     the CLI surface.
package cluster
