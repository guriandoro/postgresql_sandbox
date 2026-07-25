// Unit tests for the MED-11 SQL quoting helpers. The end-to-end
// behavior (exact statements handed to the fake runner) is covered in
// logical_test.go / replication_test.go; these pin down the helpers'
// edge cases directly.

package sandbox

import "testing"

func TestQuoteLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "'plain'"},
		{"", "''"},
		{"o'brien", "'o''brien'"},
		{"''", "''''''"},
		// standard_conforming_strings=on: backslash stays as-is.
		{`back\slash`, `'back\slash'`},
	}
	for _, c := range cases {
		if got := quoteLiteral(c.in); got != c.want {
			t.Errorf("quoteLiteral(%q): got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"t1", `"t1"`},
		{"sub1_sub", `"sub1_sub"`},
		{`we"ird`, `"we""ird"`},
		{"Mixed Case", `"Mixed Case"`},
	}
	for _, c := range cases {
		if got := quoteIdent(c.in); got != c.want {
			t.Errorf("quoteIdent(%q): got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestQuoteConninfoValue(t *testing.T) {
	cases := []struct{ in, want string }{
		// Plain values pass through verbatim (common case: statements
		// render exactly as before MED-11).
		{"postgres", "postgres"},
		{"127.0.0.1", "127.0.0.1"},
		// Empty and special values get libpq single-quoting.
		{"", "''"},
		{"o'brien", `'o\'brien'`},
		{`a\b`, `'a\\b'`},
		{"two words", "'two words'"},
	}
	for _, c := range cases {
		if got := quoteConninfoValue(c.in); got != c.want {
			t.Errorf("quoteConninfoValue(%q): got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestRequotePublicationTableValid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"t1", "t1"},
		{"public.t1", "public.t1"},
		// Bare identifiers are emitted verbatim; the server lower-case
		// folds them exactly as if typed into psql.
		{"T1", "T1"},
		{"a$1", "a$1"},
		{"_x", "_x"},
		// Quoted identifiers round-trip.
		{`"Weird Table"`, `"Weird Table"`},
		{`public."Weird Table"`, `public."Weird Table"`},
		{`"My""Schema".t2`, `"My""Schema".t2`},
		{`"CamelSchema"."T2"`, `"CamelSchema"."T2"`},
	}
	for _, c := range cases {
		got, err := requotePublicationTable(c.in)
		if err != nil {
			t.Errorf("requotePublicationTable(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("requotePublicationTable(%q): got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestRequotePublicationTableRejected(t *testing.T) {
	cases := []string{
		"t1; DROP DATABASE app; --",
		`"quoted;name"`, // ';' rejected even inside quotes
		"",
		"a.b.c",
		`"unterminated`,
		`""`,   // empty quoted identifier
		".t1",  // empty first part
		"t1.",  // empty second part
		"a..b", // empty middle part
		"1abc", // bare identifier cannot start with a digit
		"foo bar",
		"t1,t2",
		`"a"x`, // trailing garbage after closing quote
	}
	for _, in := range cases {
		if got, err := requotePublicationTable(in); err == nil {
			t.Errorf("requotePublicationTable(%q): expected error, got %s", in, got)
		}
	}
}
