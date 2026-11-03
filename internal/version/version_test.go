package version

import (
	"math/rand"
	"regexp"
	"slices"
	"testing"
)

// sortV is the order `LC_ALL=C sort -V` produces for these tags.
var sortV = []string{
	".",
	"..",
	".hidden",
	"0",
	"00",
	"1~rc1",
	"1",
	"1a",
	"1.0~beta",
	"1.0",
	"1.0-rc1",
	"1.0.0",
	"1.001",
	"1.01",
	"1.1",
	"1.2~",
	"1.2.X",
	"1.2.tar.gz",
	"1.2.x",
	"1.2a",
	"1.2+x",
	"1.2.3.tar.gz",
	"1_2",
	"2.9.0",
	"2.10.19",
	"2.11.182",
	"7-alpine3.20",
	"7.4.11-alpine",
	"9",
	"10",
	"13-slim",
	"18-alpine",
	"18.1-alpine",
	// The suffix's leading digit run is read as a number: 9 < 777.
	"2026.8.22-9fea41204",
	"2026.8.22-777ba8fa4",
	"a",
	"b",
	"v1.0",
	"v2026.8.13",
	"v2026.8.18",
	"v2026.10.1",
}

func TestCompareMatchesSortV(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for range 20 {
		got := slices.Clone(sortV)
		r.Shuffle(len(got), func(i, j int) { got[i], got[j] = got[j], got[i] })
		slices.SortFunc(got, Compare)
		if !slices.Equal(got, sortV) {
			t.Fatalf("order differs from sort -V:\n got  %q\n want %q", got, sortV)
		}
	}
}

func TestNewest(t *testing.T) {
	semver := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	tags := []string{"2.9.0", "latest", "2.11.182", "2.10.19", "2.11.182-rc1"}
	if got, ok := Newest(tags, semver, OrderVersion); !ok || got != "2.11.182" {
		t.Errorf("version order: %q %v", got, ok)
	}
	// Pushed order trusts the listing's order: first match wins.
	pushed := []string{"latest", "2026.8.22-9fea41204", "2026.8.22-777ba8fa4"}
	daily := regexp.MustCompile(`^[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2}-[0-9a-f]{9}$`)
	if got, _ := Newest(pushed, daily, OrderPushed); got != "2026.8.22-9fea41204" {
		t.Errorf("pushed order: %q", got)
	}
	if _, ok := Newest(tags, daily, OrderVersion); ok {
		t.Error("expected no match")
	}
}

func TestDrift(t *testing.T) {
	calver := regexp.MustCompile(`^v[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2}(\.[0-9]+)?$`)
	semver := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	cases := []struct {
		name   string
		re     *regexp.Regexp
		pushed []string
		want   []string
	}{
		{
			// Upstream moved from CalVer to semver; old CalVer tags still match.
			"scheme change", calver,
			[]string{"main", "latest", "v0.22.6", "v0.22.6-desktop", "rc.3-v0.22.6", "v0.22.5", "v2026.9.24", "v0.1.0"},
			[]string{"v0.22.6", "v0.22.5"},
		},
		{
			// Floating aliases and rc tags ahead of the newest release are normal.
			"aliases and prereleases", semver,
			[]string{"edge", "2.1", "2", "latest", "2.1.8-rc.1", "rc", "2.1.7", "2.1.6"},
			nil,
		},
		{"newest push matches", semver, []string{"2.1.7", "v9.9.9"}, nil},
		{"nothing matches", semver, []string{"v1.2.3", "latest"}, []string{"v1.2.3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Drift(c.pushed, c.re); !slices.Equal(got, c.want) {
				t.Errorf("Drift = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGuard(t *testing.T) {
	cases := []struct {
		cur, next, order string
		ok               bool
	}{
		{"2.11.182", "2.11.212", OrderVersion, true},
		{"2.11.182", "2.11.182", OrderVersion, true},
		{"2.11.182", "2.10.19", OrderVersion, false},
		// Same-day rebuild whose suffix sorts lower: allowed under pushed.
		{"2026.8.22-777ba8fa4", "2026.8.22-9fea41204", OrderPushed, true},
		{"2026.8.28-a30b2d474", "2026.7.1-0123456789", OrderPushed, false},
		{"2026.8.28-a30b2d474", "2026.9.2-0123456789", OrderPushed, true},
	}
	for _, c := range cases {
		if err := Guard(c.cur, c.next, c.order); (err == nil) != c.ok {
			t.Errorf("Guard(%q, %q, %s) = %v, want ok=%v", c.cur, c.next, c.order, err, c.ok)
		}
	}
}
