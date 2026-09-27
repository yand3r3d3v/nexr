package retention

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is a tag read as a version number: SemVer 2.0 with an optional "v"
// prefix, where the minor and patch numbers may be omitted (1, 1.2, v1.2.3,
// 1.2.3-rc.1). Leading zeros are allowed (2026.09.01). Build metadata cannot
// occur, because tags cannot contain "+".
type Version struct {
	nums [3]uint64
	pre  []string
}

var versionRE = regexp.MustCompile(`^[vV]?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// ParseVersion reads a tag as a version; ok is false for other tags.
func ParseVersion(tag string) (v Version, ok bool) {
	m := versionRE.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, false
	}
	for i := range 3 {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return Version{}, false // too large
		}
		v.nums[i] = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	return v, true
}

// Compare returns -1, 0 or +1 when v has lower, equal or higher precedence
// than w, following SemVer: a pre-release is lower than its release, and
// pre-release identifiers compare numerically when they are numbers.
func (v Version) Compare(w Version) int {
	for i := range 3 {
		if v.nums[i] != w.nums[i] {
			return cmp(v.nums[i] < w.nums[i])
		}
	}
	switch {
	case len(v.pre) == 0 && len(w.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(w.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(w.pre); i++ {
		a, b := v.pre[i], w.pre[i]
		an, aNum := number(a)
		bn, bNum := number(b)
		switch {
		case aNum && bNum && an != bn:
			return cmp(an < bn)
		case aNum != bNum:
			return cmp(aNum) // numeric identifiers are lower
		case !aNum && a != b:
			return cmp(a < b)
		}
	}
	if len(v.pre) != len(w.pre) {
		return cmp(len(v.pre) < len(w.pre))
	}
	return 0
}

func cmp(less bool) int {
	if less {
		return -1
	}
	return 1
}

func number(s string) (uint64, bool) {
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}
