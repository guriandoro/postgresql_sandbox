// Package report drives the pg_gather HTML report generation
// pipeline: stand up a throwaway sandbox, load the gather schema,
// ingest a captured out.txt, render the report, tear the sandbox
// down.
//
// The captured out.txt usually comes from someone else's system (a
// customer support bundle) and is therefore treated as UNTRUSTED
// input: it is pre-scanned and refused if it contains psql
// meta-commands or COPY PROGRAM constructs a genuine pg_gather
// capture never produces. See the trust-model comment block on
// scanGatherInput in report.go.
//
// See SPEC.md §6.13 for the contract this package implements.
package report
