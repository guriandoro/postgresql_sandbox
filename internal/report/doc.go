// Package report drives the pg_gather HTML report generation
// pipeline: stand up a throwaway sandbox, load the gather schema,
// ingest a captured out.txt, render the report, tear the sandbox
// down.
//
// See SPEC.md §6.13 for the contract this package implements.
package report
