package policy

import (
	"path"
	"strings"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// Asset patterns.
//
// ReleaseGraph supports a deliberately small pattern language: literal
// characters, `*` (any run, including empty) and `?` (exactly one character).
// Everything else is rejected by ValidatePattern.
//
// The reason is parity, and it is measured rather than assumed. Python's
// `fnmatch` and Go's `path.Match` are NOT the same language: `[!a]` means "not
// a" to fnmatch and "the literal characters ! and a" to path.Match, and `[^a]`
// means the exact opposite pair. Python 3.14's fnmatch also compiles to atomic
// groups and lookaheads, which Go's RE2 cannot express at all, so porting it is
// impossible. Restricting the language to the region where both engines provably
// agree is what makes the two implementations comparable at all:
// `testdata/health/patterns.json` pins 236,496 (pattern, name) pairs that
// Python and Go are both required to match identically.
//
// The restriction costs nothing today: all 48 required patterns across the 16
// managed repositories use only literals and `*`.
//
// A repository that genuinely needs a character class has to extend the language
// here, on both sides at once, with the shared fixture extended in the same
// commit. That is the point: the boundary is loud instead of silently
// disagreeing.

// patternReserved lists the characters the pattern language does not use. `\` is
// reserved because there is no way to quote a metacharacter, and a policy that
// looks like it escapes something must not be read as if it did.
const patternReserved = "\\/[]!^"

// ValidatePattern rejects any pattern outside the supported language.
func ValidatePattern(pattern string) error {
	if pattern == "" {
		return rgerrors.New(rgerrors.Policy, "asset patterns must be non-empty")
	}
	for _, r := range pattern {
		if strings.ContainsRune(patternReserved, r) {
			return rgerrors.New(rgerrors.Policy, "asset pattern "+pattern+
				" uses reserved character "+string(r)+
				"; supported syntax is literals, * and ?")
		}
		if r < 0x20 || r == 0x7f {
			return rgerrors.New(rgerrors.Policy, "asset pattern "+pattern+" contains a control character")
		}
	}
	return nil
}

// MatchAsset reports whether a released asset name satisfies a required pattern.
//
// The pattern must already satisfy ValidatePattern; it is validated again here
// because a silently different answer is worse than an error, and this function
// is the one place both callers go through.
func MatchAsset(pattern, name string) (bool, error) {
	if err := ValidatePattern(pattern); err != nil {
		return false, err
	}
	matched, err := path.Match(pattern, name)
	if err != nil {
		// Unreachable while the pattern language excludes `[` and `\`, which are
		// the only inputs path.Match rejects. Guarded rather than ignored so a
		// future language extension cannot silently turn into "no match".
		return false, rgerrors.New(rgerrors.Policy, "asset pattern "+pattern+" is not matchable: "+err.Error())
	}
	return matched, nil
}
