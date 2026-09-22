// Package version selects the newest upstream tag and guards against moving
// a pin backwards.
package version

import (
	"fmt"
	"regexp"
	"strings"
)

// Compare orders two tags the way GNU `sort -V` does: gnulib's filevercmp,
// with a plain byte comparison as the last resort for strings filevercmp
// considers equal (e.g. "1.01" and "1.1").
func Compare(a, b string) int {
	if c := filevercmp(a, b); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// GTE reports whether a sorts at or after b.
func GTE(a, b string) bool { return Compare(a, b) >= 0 }

// Order is the x-update `order:` knob.
const (
	OrderVersion = "version"
	OrderPushed  = "pushed"
)

// Newest picks the newest tag matching re. For OrderPushed, tags must be
// newest-first and the first match wins; for OrderVersion, the highest match
// by Compare wins.
func Newest(tags []string, re *regexp.Regexp, order string) (string, bool) {
	best, found := "", false
	for _, t := range tags {
		if !re.MatchString(t) {
			continue
		}
		if order == OrderPushed {
			return t, true
		}
		if !found || Compare(t, best) > 0 {
			best, found = t, true
		}
	}
	return best, found
}

// Guard refuses a newest tag that sorts older than the current pin, which
// means a truncated upstream list or a changed tag scheme.
//
// For OrderPushed only the dotted core before the first '-' is compared.
// Those tags carry an opaque build suffix (2026.8.28-a30b2d474), and version
// sort reads the suffix's leading digit run as a number, so two builds of one
// day order arbitrarily. Push time is the authority for them; equal cores are
// same-day rebuilds and may move forward, while an older core still fails.
func Guard(cur, next, order string) error {
	gc, gn := cur, next
	if order == OrderPushed {
		gc, _, _ = strings.Cut(cur, "-")
		gn, _, _ = strings.Cut(next, "-")
	}
	if gc != gn && !GTE(gn, gc) {
		return fmt.Errorf("upstream newest (%s) is older than current pin (%s) — upstream list looks truncated or the tag scheme changed", next, cur)
	}
	return nil
}

// --- gnulib filevercmp, ported ---

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
func isAlpha(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// filePrefixLen returns the length of s without its longest suffix matching
// (\.[A-Za-z~][A-Za-z0-9~]*)*$ .
func filePrefixLen(s string) int {
	n := len(s)
	prefix := 0
	for i := 0; ; {
		if i == n {
			return prefix
		}
		i++
		prefix = i
		for i+1 < n && s[i] == '.' && (isAlpha(s[i+1]) || s[i+1] == '~') {
			for i += 2; i < n && (isAlpha(s[i]) || isDigit(s[i]) || s[i] == '~'); i++ {
			}
		}
	}
}

// order ranks s[pos] for comparison: end of string before everything except
// '~', letters before other non-digits.
func order(s string, pos int) int {
	if pos == len(s) {
		return -1
	}
	c := s[pos]
	switch {
	case isDigit(c):
		return 0
	case isAlpha(c):
		return int(c)
	case c == '~':
		return -2
	default:
		return int(c) + 256
	}
}

// verrevcmp is dpkg's version comparison.
func verrevcmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		firstDiff := 0
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			ac, bc := order(a, i), order(b, j)
			if ac != bc {
				return ac - bc
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for i < len(a) && j < len(b) && isDigit(a[i]) && isDigit(b[j]) {
			if firstDiff == 0 {
				firstDiff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if firstDiff != 0 {
			return firstDiff
		}
	}
	return 0
}

func filevercmp(a, b string) int {
	if a == "" {
		if b == "" {
			return 0
		}
		return -1
	}
	if b == "" {
		return 1
	}

	// "." sorts first, then "..", then other names with a leading ".", then
	// everything else.
	if a[0] == '.' {
		if b[0] != '.' {
			return -1
		}
		ad, bd := a == ".", b == "."
		if ad || bd {
			return boolCmp(bd, ad)
		}
		add, bdd := a == "..", b == ".."
		if add || bdd {
			return boolCmp(bdd, add)
		}
	} else if b[0] == '.' {
		return 1
	}

	ap, bp := filePrefixLen(a), filePrefixLen(b)
	onePass := ap == len(a) && bp == len(b)
	if r := verrevcmp(a[:ap], b[:bp]); r != 0 || onePass {
		return r
	}
	return verrevcmp(a, b)
}

// boolCmp returns -1 when only the first special case holds for a (i.e. a
// sorts first), 1 when only b's does, 0 when both.
func boolCmp(bSpecial, aSpecial bool) int {
	switch {
	case aSpecial && bSpecial:
		return 0
	case aSpecial:
		return -1
	default:
		return 1
	}
}
