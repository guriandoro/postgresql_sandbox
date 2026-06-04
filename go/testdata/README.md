# testdata

Test fixtures live here. The `testdata/` name is special: the `go`
tool ignores this directory when building, but `go test` makes its
contents available via `t.TempDir()` patterns and plain file reads.

Empty for now; populated as test suites are added.
