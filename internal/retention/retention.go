// Package retention decides which tags of an image a retention policy deletes
// (spec FR-DRM-2). It has no I/O: the same decisions drive the dry run, the
// confirmation, the deletion and the JSON output, so what is shown is what is
// executed.
package retention

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yand3r3d3v/nexr/internal/remote"
)

// SortKey orders tags. The tags that come first count as the newest: they are
// the ones --keep keeps.
type SortKey string

// Sort orders.
const (
	SortPushed SortKey = "pushed" // push time, newest first
	SortSemVer SortKey = "semver" // version, highest first; other tags are never deleted
	SortName   SortKey = "name"   // name, last in byte order first (e.g. date stamps)
)

// ParseSort reads a --sort value.
func ParseSort(s string) (SortKey, error) {
	switch k := SortKey(s); k {
	case SortPushed, SortSemVer, SortName:
		return k, nil
	}
	return "", fmt.Errorf("invalid sort order %q: use pushed, semver or name", s)
}

// Action is what happens to a tag.
type Action string

// Actions.
const (
	Keep   Action = "keep"
	Delete Action = "delete"
	Skip   Action = "skip" // not a version, with --sort semver: never deleted
)

// Tag is a tag of an image.
type Tag struct {
	Name   string
	Pushed time.Time // zero when the tag is not in the search index yet
}

// Policy selects the tags to delete. At least one of Keep, OlderThan and All
// is set; Keep and All exclude each other.
type Policy struct {
	Keep      int              // keep the first Keep candidates; 0 = not set
	OlderThan time.Duration    // keep candidates pushed less than this ago; 0 = not set
	All       bool             // delete every candidate that OlderThan does not keep
	Match     []remote.Pattern // the candidates; every tag when empty
	Exclude   []remote.Pattern // protected tags: never deleted, not counted by Keep
	Sort      SortKey          // default SortPushed
	Now       time.Time
}

// Decision is the verdict for one tag.
type Decision struct {
	Tag    Tag
	Action Action
	Reason string // "protected (latest)", "newest 2", "beyond newest 2", …
}

// Plan decides for every tag and returns the decisions in sort order, newest
// first.
func Plan(tags []Tag, p Policy) ([]Decision, error) {
	switch {
	case p.Keep < 0:
		return nil, errors.New("the number of tags to keep must not be negative")
	case p.Keep > 0 && p.All:
		return nil, errors.New("keeping some tags and deleting all tags exclude each other")
	case p.Keep == 0 && p.OlderThan <= 0 && !p.All:
		return nil, errors.New("the policy selects no tags: set a number to keep, a minimum age or all")
	}
	if p.Sort == "" {
		p.Sort = SortPushed
	}
	sorted := append([]Tag(nil), tags...)
	Sort(sorted, p.Sort)

	decisions := make([]Decision, 0, len(sorted))
	rank := 0
	for _, t := range sorted {
		d := Decision{Tag: t, Action: Keep}
		_, isVersion := ParseVersion(t.Name)
		switch {
		case len(p.Match) > 0 && !remote.MatchAny(p.Match, t.Name):
			d.Reason = "not matched by --match"
		case protectedBy(p.Exclude, t.Name) != "":
			d.Reason = "protected (" + protectedBy(p.Exclude, t.Name) + ")"
		case t.Pushed.IsZero():
			// Not indexed yet: eventual consistency may only make nexr delete less.
			d.Reason = "not in the search index yet"
		case p.Sort == SortSemVer && !isVersion:
			d.Action, d.Reason = Skip, "not a version"
		default:
			rank++
			d.Action, d.Reason = p.decide(rank, t)
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

func protectedBy(patterns []remote.Pattern, name string) string {
	for _, x := range patterns {
		if x.Match(name) {
			return x.String()
		}
	}
	return ""
}

// decide applies the policy to the candidate at position rank (1 = newest).
func (p Policy) decide(rank int, t Tag) (Action, string) {
	first := map[SortKey]string{SortPushed: "newest", SortSemVer: "highest", SortName: "last by name"}[p.Sort]
	young := p.OlderThan > 0 && p.Now.Sub(t.Pushed) < p.OlderThan
	switch {
	case p.Keep > 0 && rank <= p.Keep:
		return Keep, fmt.Sprintf("%s %d", first, p.Keep)
	case young:
		return Keep, "pushed within " + FormatAge(p.OlderThan)
	}
	var why []string
	if p.Keep > 0 {
		why = append(why, fmt.Sprintf("beyond %s %d", first, p.Keep))
	}
	if p.OlderThan > 0 {
		why = append(why, "older than "+FormatAge(p.OlderThan))
	}
	if len(why) == 0 {
		why = append(why, "all tags selected")
	}
	return Delete, strings.Join(why, ", ")
}

// FormatAge formats a duration in days when it is a whole number of days.
func FormatAge(d time.Duration) string {
	const day = 24 * time.Hour
	if d >= day && d%day == 0 {
		return fmt.Sprintf("%dd", d/day)
	}
	return d.String()
}

// Sort orders tags by key, newest first (see Compare).
func Sort(tags []Tag, key SortKey) {
	sort.SliceStable(tags, func(i, j int) bool { return Compare(tags[i], tags[j], key) < 0 })
}

// Compare orders two tags by key: negative when a comes first, that is, when
// it counts as newer. Ties are ordered by name. Tags that are not in the
// search index yet were pushed a moment ago and come first when sorting by
// push time; with SortSemVer, tags that are not versions come last, sorted by
// name.
func Compare(a, b Tag, key SortKey) int {
	switch key {
	case SortSemVer:
		va, aok := ParseVersion(a.Name)
		vb, bok := ParseVersion(b.Name)
		switch {
		case aok != bok:
			return cmp(aok) // versions first
		case !aok:
			return strings.Compare(a.Name, b.Name)
		}
		if c := va.Compare(vb); c != 0 {
			return -c
		}
	case SortName:
	default:
		switch {
		case a.Pushed.Equal(b.Pushed):
		case a.Pushed.IsZero():
			return -1
		case b.Pushed.IsZero():
			return 1
		case a.Pushed.After(b.Pushed):
			return -1
		default:
			return 1
		}
	}
	return -strings.Compare(a.Name, b.Name)
}
