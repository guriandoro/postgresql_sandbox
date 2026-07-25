// SQL quoting helpers (MED-11).
//
// Everything in this package that reaches a server does so through
// `psql -c "<statement>"` with the statement built by string
// concatenation. Most interpolated values are identifier-folded at
// create time (sanitizeSQLIdentifier, config.NormalizeString), but
// values read back from a sandbox's config file — slot names,
// subscription names — bypass those on hand-edited configs, and the
// publish/subscribe paths splice user-supplied table lists and libpq
// conninfo strings directly into SQL. These helpers make every such
// splice safe at use time.
//
// A note on quoteLiteral and backslashes: sandboxes are always initdb'd
// with vendor defaults, where standard_conforming_strings=on (the
// default since PostgreSQL 9.1). Inside a standard-conforming '...'
// literal a backslash is an ordinary character, so doubling embedded
// single quotes is the complete escaping rule — we deliberately do NOT
// double backslashes, which would corrupt values on every server this
// tool can create.

package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// quoteLiteral returns s as a SQL string literal: wrapped in single
// quotes with embedded single quotes doubled. See the file-level
// comment for why backslashes need no treatment here.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// quoteIdent returns s as a double-quoted SQL identifier with embedded
// double quotes doubled. Quoted identifiers are case-sensitive, so
// callers that previously emitted a bare identifier must only pass
// names that are already lower-case (true for everything produced by
// sanitizeSQLIdentifier / config.NormalizeString).
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteConninfoValue quotes a single value for a libpq keyword=value
// connection string. Plain values — non-empty, no whitespace, no
// quote, no backslash — are returned verbatim so the common case
// renders exactly as before. Everything else is wrapped in single
// quotes with libpq's escapes applied: `\` becomes `\\` and `'`
// becomes `\'`.
func quoteConninfoValue(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r'\\") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(s) + "'"
}

// requotePublicationTable validates one publish --tables item and
// re-emits it in a form safe to splice into CREATE PUBLICATION ...
// FOR TABLE. The accepted grammar is `(schema.)?name` where each part
// is either a bare identifier ([A-Za-z_][A-Za-z0-9_$]*, emitted
// verbatim so PostgreSQL's lower-case folding behaves exactly as if
// the user typed it in psql) or a double-quoted identifier (`""` for
// an embedded quote), re-emitted via quoteIdent. Anything else —
// semicolons, empty parts, three-part names, stray characters after a
// closing quote — is rejected with an error naming the offending item.
func requotePublicationTable(item string) (string, error) {
	// Explicit `;` check first: the parse below rejects it anyway,
	// but "';' is not allowed" is a much clearer error for the
	// obvious injection/copy-paste case than "invalid character".
	if strings.Contains(item, ";") {
		return "", fmt.Errorf("table %q: ';' is not allowed", item)
	}
	var parts []string
	rest := item
	for {
		part, tail, err := parseTableIdentPart(rest)
		if err != nil {
			return "", fmt.Errorf("table %q: %w", item, err)
		}
		parts = append(parts, part)
		if tail == "" {
			break
		}
		if tail[0] != '.' {
			return "", fmt.Errorf("table %q: unexpected %q after identifier", item, tail[0])
		}
		rest = tail[1:]
	}
	if len(parts) > 2 {
		return "", fmt.Errorf("table %q: at most schema.name qualification is allowed", item)
	}
	return strings.Join(parts, "."), nil
}

// parseTableIdentPart consumes one identifier (bare or double-quoted)
// from the front of s and returns its safe-to-emit form plus the
// unconsumed remainder. Bare identifiers are returned verbatim (they
// match a character class with no SQL metacharacters); quoted ones are
// unescaped and re-emitted via quoteIdent.
func parseTableIdentPart(s string) (emit, rest string, err error) {
	if s == "" {
		return "", "", errors.New("empty identifier")
	}
	if s[0] == '"' {
		var content strings.Builder
		i := 1
		for i < len(s) {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					content.WriteByte('"')
					i += 2
					continue
				}
				if content.Len() == 0 {
					return "", "", errors.New("empty quoted identifier")
				}
				return quoteIdent(content.String()), s[i+1:], nil
			}
			content.WriteByte(s[i])
			i++
		}
		return "", "", errors.New("unterminated quoted identifier")
	}
	i := 0
	for i < len(s) && isBareTableIdentChar(s[i], i == 0) {
		i++
	}
	if i == 0 {
		return "", "", fmt.Errorf("invalid identifier character %q", s[0])
	}
	return s[:i], s[i:], nil
}

// isBareTableIdentChar reports whether c may appear in a bare
// (unquoted) identifier at the given position. ASCII-only on purpose:
// PostgreSQL accepts some non-ASCII bytes in bare identifiers, but
// anyone using them can (and should) double-quote the name.
func isBareTableIdentChar(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		return true
	case first:
		return false
	case c >= '0' && c <= '9', c == '$':
		return true
	}
	return false
}
