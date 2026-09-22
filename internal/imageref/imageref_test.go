package imageref

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Ref
	}{
		{"redis", Ref{"docker.io", "library/redis", "latest", ""}},
		{"docker.io/redis:7-alpine", Ref{"docker.io", "library/redis", "7-alpine", ""}},
		{"nousresearch/hermes-agent:v2026.8.18", Ref{"docker.io", "nousresearch/hermes-agent", "v2026.8.18", ""}},
		{"ghcr.io/firecrawl/firecrawl:2.11.212", Ref{"ghcr.io", "firecrawl/firecrawl", "2.11.212", ""}},
		// Tag and digest together: the tag survives, the digest is kept apart.
		{"docker.io/debian:13-slim@sha256:d7e1", Ref{"docker.io", "library/debian", "13-slim", "sha256:d7e1"}},
		{"ghcr.io/block/buzz:main@sha256:7964", Ref{"ghcr.io", "block/buzz", "main", "sha256:7964"}},
		// A registry port is not a tag.
		{"registry.local:5000/team/app", Ref{"registry.local:5000", "team/app", "latest", ""}},
		{"registry.local:5000/team/app:1.2", Ref{"registry.local:5000", "team/app", "1.2", ""}},
		{"localhost/app:dev", Ref{"localhost", "app", "dev", ""}},
		{"quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z", Ref{"quay.io", "minio/minio", "RELEASE.2025-09-07T16-13-09Z", ""}},
	}
	for _, c := range cases {
		if got := Parse(c.in); got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestJoinAndPinOf(t *testing.T) {
	d := "sha256:858f009f9709ce576febc734aa78b8f6d624b82571f9ddb6bda4377c833b3499"
	if got := Join("library/redis", d); got != "library/redis@"+d {
		t.Errorf("Join digest = %q", got)
	}
	if got := Join("searxng/searxng", "2026.8.28-a30b2d474"); got != "searxng/searxng:2026.8.28-a30b2d474" {
		t.Errorf("Join tag = %q", got)
	}
	// The digest pin keeps its algorithm prefix.
	if got := PinOf("ghcr.io/block/buzz@" + d); got != d {
		t.Errorf("PinOf digest = %q", got)
	}
	if got := PinOf("docker.io/searxng/searxng:2026.8.28-a30b2d474"); got != "2026.8.28-a30b2d474" {
		t.Errorf("PinOf tag = %q", got)
	}
	if got := PinOf("registry.local:5000/team/app"); got != "" {
		t.Errorf("PinOf portless = %q", got)
	}
	if IsDigest("2.11.212") || IsDigest("sha256:short") || !IsDigest(d) {
		t.Error("IsDigest misclassifies")
	}
}
