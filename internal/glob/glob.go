// Package glob implements the branch-contract pattern language shared by
// policy validation and the branch contract checker.
//
// Supported syntax:
//
//   - "*"     matches any run of characters except "/"
//   - "**"    matches any run of characters including "/" (used by path
//     scopes such as ".github/workflows/**")
//   - "?"     matches exactly one character except "/"
//
// Character classes ("[...]") are deliberately NOT part of the language and
// are rejected, mirroring internal/policy/pattern.go: fnmatch and path.Match
// disagree about class negation, so classes would make the language ambiguous
// across implementations. The branch contract is evaluated by the Go core
// only, but the policy is validated on both sides, so the accepted syntax must
// stay trivially portable. This package is the single matcher; internal/policy
// uses it for pattern validation at load time and internal/branchcontract for
// matching at evaluation time.
package glob

import "errors"

// ErrBadPattern reports a malformed or unsupported pattern.
var ErrBadPattern = errors.New("syntax error in pattern")

// Check reports whether pattern is syntactically valid under the restricted
// language. "[", "\", "!" and "^" are reserved and rejected.
func Check(pattern string) error {
	if pattern == "" {
		return ErrBadPattern
	}
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '[', '\\', '!', '^':
			return ErrBadPattern
		case '*':
			continue
		case '?':
			continue
		}
	}
	return nil
}

// Match reports whether name matches pattern. Malformed patterns yield
// (false, ErrBadPattern); use Check to validate patterns eagerly.
func Match(pattern, name string) (bool, error) {
	if err := Check(pattern); err != nil {
		return false, err
	}
	return match(pattern, name), nil
}

func match(pattern, name string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			double := len(pattern) > 1 && pattern[1] == '*'
			rest := pattern
			for len(rest) > 0 && rest[0] == '*' {
				rest = rest[1:]
			}
			for i := 0; ; i++ {
				if match(rest, name[i:]) {
					return true
				}
				// Stop extending the wildcard: a single "*" may not cross
				// a "/" boundary; "**" may.
				if i == len(name) || (name[i] == '/' && !double) {
					return false
				}
			}
		case '?':
			if len(name) == 0 || name[0] == '/' {
				return false
			}
			pattern, name = pattern[1:], name[1:]
		default:
			if len(name) == 0 || name[0] != pattern[0] {
				return false
			}
			pattern, name = pattern[1:], name[1:]
		}
	}
	return len(name) == 0
}
