package retention

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/remote"
)

var now = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func patterns(t *testing.T, list ...string) []remote.Pattern {
	t.Helper()
	p, err := remote.ParsePatterns(list)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// tags builds tags pushed one day apart, the first one newest.
func tags(names ...string) []Tag {
	out := make([]Tag, len(names))
	for i, n := range names {
		out[i] = Tag{Name: n, Pushed: now.Add(-time.Duration(i) * 24 * time.Hour)}
	}
	return out
}

// summary renders decisions as "tag:action" in plan order.
func summary(ds []Decision) string {
	var parts []string
	for _, d := range ds {
		parts = append(parts, d.Tag.Name+":"+string(d.Action))
	}
	return strings.Join(parts, " ")
}

func reasons(ds []Decision) map[string]string {
	out := map[string]string{}
	for _, d := range ds {
		out[d.Tag.Name] = d.Reason
	}
	return out
}

// The example of FR-DRM-2.
func TestKeepNewest(t *testing.T) {
	ds, err := Plan(tags("latest", "v5", "v4", "v3", "v2", "v1"), Policy{Keep: 2, Exclude: patterns(t, "latest"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "latest:keep v5:keep v4:keep v3:delete v2:delete v1:delete" {
		t.Fatalf("plan %s", got)
	}
	r := reasons(ds)
	if r["latest"] != "protected (latest)" || r["v5"] != "newest 2" || r["v3"] != "beyond newest 2" {
		t.Fatalf("reasons %v", r)
	}
}

func TestProtectedTagsDoNotCount(t *testing.T) {
	// latest is the oldest here and still protected; it does not use up a place.
	ts := tags("v3", "v2", "v1", "latest")
	ds, err := Plan(ts, Policy{Keep: 1, Exclude: patterns(t, "latest", "re:^v1$"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "v3:keep v2:delete v1:keep latest:keep" {
		t.Fatalf("plan %s", got)
	}
	if r := reasons(ds); r["v1"] != "protected (re:^v1$)" {
		t.Fatalf("reasons %v", r)
	}
}

func TestOlderThan(t *testing.T) {
	ts := tags("d0", "d1", "d2", "d3", "d4") // pushed 0 to 4 days ago
	ds, err := Plan(ts, Policy{OlderThan: 2 * 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "d0:keep d1:keep d2:delete d3:delete d4:delete" {
		t.Fatalf("older than: %s", got)
	}
	if r := reasons(ds); r["d1"] != "pushed within 2d" || r["d2"] != "older than 2d" {
		t.Fatalf("reasons %v", r)
	}

	// Combined with --keep, a tag survives when either rule keeps it.
	ds, err = Plan(ts, Policy{Keep: 3, OlderThan: 36 * time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "d0:keep d1:keep d2:keep d3:delete d4:delete" {
		t.Fatalf("keep + older than: %s", got)
	}
	if r := reasons(ds); r["d3"] != "beyond newest 3, older than 36h0m0s" {
		t.Fatalf("reasons %v", r)
	}
}

func TestAllAndMatch(t *testing.T) {
	ts := tags("latest", "feature-x", "1.1", "feature-y", "1.0")
	ds, err := Plan(ts, Policy{All: true, Match: patterns(t, "feature-*"), Exclude: patterns(t, "latest"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "latest:keep feature-x:delete 1.1:keep feature-y:delete 1.0:keep" {
		t.Fatalf("plan %s", got)
	}
	if r := reasons(ds); r["1.1"] != "not matched by --match" || r["feature-x"] != "all tags selected" {
		t.Fatalf("reasons %v", r)
	}
}

// Tags that the search index does not know yet are never deleted
// (FR-DRM-4); by push time they are the newest.
func TestUnindexedTags(t *testing.T) {
	ts := append(tags("v2", "v1"), Tag{Name: "v3"})
	ds, err := Plan(ts, Policy{All: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "v3:keep v2:delete v1:delete" {
		t.Fatalf("plan %s", got)
	}
	if r := reasons(ds); r["v3"] != "not in the search index yet" {
		t.Fatalf("reasons %v", r)
	}
}

func TestSortSemVer(t *testing.T) {
	// Push order differs from version order.
	ts := tags("1.9.0", "latest", "2.0.0-rc.1", "v1.10.0", "2.0.0", "nightly", "1.2")
	ds, err := Plan(ts, Policy{Keep: 2, Sort: SortSemVer, Exclude: patterns(t, "latest"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	want := "2.0.0:keep 2.0.0-rc.1:keep v1.10.0:delete 1.9.0:delete 1.2:delete latest:keep nightly:skip"
	if got := summary(ds); got != want {
		t.Fatalf("plan %s\nwant %s", got, want)
	}
	if r := reasons(ds); r["2.0.0"] != "highest 2" || r["nightly"] != "not a version" || r["1.2"] != "beyond highest 2" {
		t.Fatalf("reasons %v", r)
	}
}

func TestSortName(t *testing.T) {
	ts := tags("build-20260901", "build-20260926", "build-20260915")
	ds, err := Plan(ts, Policy{Keep: 1, Sort: SortName, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(ds); got != "build-20260926:keep build-20260915:delete build-20260901:delete" {
		t.Fatalf("plan %s", got)
	}
	if r := reasons(ds); r["build-20260926"] != "last by name 1" {
		t.Fatalf("reasons %v", r)
	}
}

func TestInvalidPolicies(t *testing.T) {
	for _, p := range []Policy{{}, {Keep: -1}, {Keep: 2, All: true}} {
		if _, err := Plan(nil, p); err == nil {
			t.Errorf("%+v: no error", p)
		}
	}
	if _, err := ParseSort("size"); err == nil {
		t.Error("invalid sort accepted")
	}
	if k, err := ParseSort("semver"); err != nil || k != SortSemVer {
		t.Errorf("semver: %v, %v", k, err)
	}
}

func TestSortTiesAndOrder(t *testing.T) {
	same := now.Add(-time.Hour)
	ts := []Tag{{Name: "a", Pushed: same}, {Name: "c", Pushed: same}, {Name: "b", Pushed: now}}
	Sort(ts, SortPushed)
	var names []string
	for _, tg := range ts {
		names = append(names, tg.Name)
	}
	if !slices.Equal(names, []string{"b", "c", "a"}) {
		t.Fatalf("order %q", names)
	}
}

func TestParseVersion(t *testing.T) {
	valid := []string{"1", "1.2", "1.2.3", "v1.2.3", "V2", "1.2.3-rc.1", "2026.09.01", "1.0.0-alpha-1", "0.0.0"}
	for _, s := range valid {
		if _, ok := ParseVersion(s); !ok {
			t.Errorf("%s: not a version", s)
		}
	}
	invalid := []string{"latest", "1.2.3.4", "v", "1.2.3-", "1.2.3-rc..1", "1.x", "", "99999999999999999999999"}
	for _, s := range invalid {
		if _, ok := ParseVersion(s); ok {
			t.Errorf("%s: accepted", s)
		}
	}
	// SemVer precedence, lowest first.
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1", "v1.10", "2"}
	for i := 0; i+1 < len(order); i++ {
		a, _ := ParseVersion(order[i])
		b, _ := ParseVersion(order[i+1])
		same, _ := ParseVersion(order[i])
		if a.Compare(b) != -1 || b.Compare(a) != 1 || a.Compare(same) != 0 {
			t.Errorf("%s < %s does not hold", order[i], order[i+1])
		}
	}
	a, _ := ParseVersion("1.2")
	b, _ := ParseVersion("v1.2.0")
	if a.Compare(b) != 0 {
		t.Error("1.2 and v1.2.0 must be equal")
	}
}

func TestFormatAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * 24 * time.Hour: "30d", 36 * time.Hour: "36h0m0s", 90 * time.Minute: "1h30m0s",
	} {
		if got := FormatAge(d); got != want {
			t.Errorf("%v: %s", d, got)
		}
	}
}
