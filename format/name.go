package format

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"golang.org/x/text/unicode/norm"
)

// A name is one or more segments separated by "/", each non-empty, in Unicode NFC. It identifies
// an artifact within whatever source holds it, and is not a path in a file system. A name pattern
// has segments too: "*" in a segment matches any run of characters within it, and a segment that is
// exactly "**" matches any number of whole segments, including none. Every other character matches
// itself, and case is significant: a source that folds case does so before it names an artifact.

// matchesName is the library function matches_name(pattern, name). It is an error, so unknown,
// for a name or pattern with an empty segment, or a pattern with "**" inside a longer segment.
func matchesName(pattern, name ref.Val) ref.Val {
	p, okP := pattern.(types.String)
	n, okN := name.(types.String)
	if !okP || !okN {
		return types.NoSuchOverloadErr()
	}
	segments, err := nameSegments(string(n), "name")
	if err != nil {
		return types.NewErr("%v", err)
	}
	patterns, err := nameSegments(string(p), "name pattern")
	if err != nil {
		return types.NewErr("%v", err)
	}
	for _, s := range patterns {
		if s != "**" && strings.Contains(s, "**") {
			return types.NewErr("name pattern %q: ** must be a whole segment", string(p))
		}
	}
	return types.Bool(matchSegments(patterns, segments))
}

// nameSegments splits a name or pattern into its segments after NFC normalization, so that two
// spellings of one name match alike. It refuses an empty segment.
func nameSegments(s, what string) ([]string, error) {
	parts := strings.Split(norm.NFC.String(s), "/")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("%s %q has an empty segment", what, s)
		}
	}
	return parts, nil
}

// matchSegments matches pattern segments against name segments. A "**" segment takes any number
// of name segments, so on a mismatch the last "**" is asked to take one more.
func matchSegments(pattern, name []string) bool {
	p, n := 0, 0
	star, resume := -1, 0
	for n < len(name) {
		switch {
		case p < len(pattern) && pattern[p] == "**":
			star, resume = p, n
			p++
		case p < len(pattern) && matchSegment(pattern[p], name[n]):
			p++
			n++
		case star >= 0:
			resume++
			p, n = star+1, resume
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == "**" {
		p++
	}
	return p == len(pattern)
}

// matchSegment matches one segment against a segment pattern, in which "*" matches any run of
// characters, the empty run included.
func matchSegment(pattern, s string) bool {
	pr, sr := []rune(pattern), []rune(s)
	p, n := 0, 0
	star, resume := -1, 0
	for n < len(sr) {
		switch {
		case p < len(pr) && pr[p] == '*':
			star, resume = p, n
			p++
		case p < len(pr) && pr[p] == sr[n]:
			p++
			n++
		case star >= 0:
			resume++
			p, n = star+1, resume
		default:
			return false
		}
	}
	for p < len(pr) && pr[p] == '*' {
		p++
	}
	return p == len(pr)
}
