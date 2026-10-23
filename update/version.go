package update

import (
	"strconv"
	"strings"
)

// Compare orders two version strings: -1 when a < b, 0 when equal, 1 when a > b.
//
// It understands the shapes used by calendar versioned daemons (2025.8.1) and
// semver (1.2.3, v1.2.3-beta.1, 1.2.3+build). Numeric segments compare
// numerically, everything else compares as text, and a missing segment counts
// as 0 so that 1.2 == 1.2.0. A prerelease suffix sorts before the release:
// 1.2.3-beta < 1.2.3.
func Compare(a, b string) int {
	as := tokenizeVersion(a)
	bs := tokenizeVersion(b)

	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}

	for i := 0; i < n; i++ {
		// A missing segment counts as 0, so 1.2.3 == 1.2.3.0; but a remaining
		// prerelease tag makes the shorter version the release and therefore
		// greater (1.2.3 > 1.2.3-rc1).
		if i >= len(as) {
			if !isNumeric(bs[i]) {
				return 1
			}
			if value, _ := toInt(bs[i]); value != 0 {
				return -1
			}
			continue
		}
		if i >= len(bs) {
			if !isNumeric(as[i]) {
				return -1
			}
			if value, _ := toInt(as[i]); value != 0 {
				return 1
			}
			continue
		}

		if cmp := compareSegment(as[i], bs[i]); cmp != 0 {
			return cmp
		}
	}
	return 0
}

func compareSegment(a, b string) int {
	an, aNumeric := toInt(a)
	bn, bNumeric := toInt(b)

	switch {
	case aNumeric && bNumeric:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	case aNumeric:
		// Numeric segments sort after textual ones, per semver build metadata.
		return 1
	case bNumeric:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

func toInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func isNumeric(s string) bool {
	_, ok := toInt(s)
	return ok
}

// tokenizeVersion drops the leading v and build metadata, then splits on the
// usual separators.
func tokenizeVersion(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexByte(v, '+'); idx >= 0 {
		v = v[:idx]
	}
	if v == "" {
		return nil
	}
	return strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
}
