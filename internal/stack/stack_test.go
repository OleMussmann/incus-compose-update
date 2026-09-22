package stack

import (
	"slices"
	"testing"
)

const rendered = `name: buzz
services:
  minio:
    image: quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z
  minio-init:
    image: quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z
    depends_on:
      minio:
        condition: service_healthy
        required: true
  postgres:
    image: docker.io/postgres:18-alpine@sha256:77f5
    x-update:
      mode: digest
      volume: vol-pgdata
      version_cmd: [postgres, --version]
  redis:
    image: docker.io/redis:7-alpine@sha256:858f
    x-update:
      mode: digest
      volume: vol-redisdata
  relay:
    image: ghcr.io/block/buzz:main@sha256:7964
    depends_on:
      minio: {condition: service_healthy}
      minio-init: {condition: service_healthy}
      postgres: {condition: service_healthy}
      redis: {condition: service_healthy}
    x-update:
      mode: digest
      version_cmd: [buzz-relay, --version]
volumes:
  pgdata: {}
`

func TestParseRendered(t *testing.T) {
	r, err := ParseRendered([]byte(rendered))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "buzz" || len(r.Services) != 5 {
		t.Fatalf("name %q, %d services", r.Name, len(r.Services))
	}
	if s := r.Service("minio"); s.Managed {
		t.Error("minio has no x-update but is Managed")
	}
	u, err := r.Service("postgres").Update()
	if err != nil {
		t.Fatal(err)
	}
	if u.Mode != "digest" || u.Volume != "vol-pgdata" || u.Order != "version" ||
		!slices.Equal(u.VersionCmd, []string{"postgres", "--version"}) {
		t.Errorf("postgres update = %+v", u)
	}
	// Defaults: volume none, version_cmd [<svc>, --version].
	u, _ = r.Service("redis").Update()
	if !slices.Equal(u.VersionCmd, []string{"redis", "--version"}) {
		t.Errorf("redis version_cmd default = %v", u.VersionCmd)
	}
	u, _ = r.Service("relay").Update()
	if u.Volume != "none" {
		t.Errorf("relay volume default = %q", u.Volume)
	}
	if !slices.Equal(r.Service("relay").DependsOn, []string{"minio", "minio-init", "postgres", "redis"}) {
		t.Errorf("relay depends_on = %v", r.Service("relay").DependsOn)
	}
	if _, err := r.Service("minio").Update(); err == nil || err.Error() != "service 'minio' has no x-update: block" {
		t.Errorf("unmanaged Update error = %v", err)
	}
}

func TestParseRenderedLegacyProjectWrapper(t *testing.T) {
	r, err := ParseRendered([]byte("project:\n  name: old\n  services:\n    a:\n      image: x\n"))
	if err != nil || r.Name != "old" || len(r.Services) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestUpdateValidation(t *testing.T) {
	cases := map[string]string{
		"missing tag_re": "services:\n  s:\n    x-update: {mode: tag}\n",
		"bad regex":      "services:\n  s:\n    x-update: {tag_re: '(('}\n",
		"bad mode":       "services:\n  s:\n    x-update: {mode: floating}\n",
		"bad order":      "services:\n  s:\n    x-update: {mode: digest, order: newest}\n",
	}
	for name, doc := range cases {
		r, err := ParseRendered([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := r.Service("s").Update(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// x-update: null is not managed.
	r, _ := ParseRendered([]byte("services:\n  s:\n    x-update: null\n"))
	if r.Service("s").Managed {
		t.Error("x-update: null counted as managed")
	}
}

func TestDependencyOrder(t *testing.T) {
	r, _ := ParseRendered([]byte(rendered))
	got, ok := r.DependencyOrder()
	want := []string{"minio", "minio-init", "postgres", "redis", "relay"}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("got %v %v", got, ok)
	}

	// A dependent that renders first still comes after its dependency.
	r, _ = ParseRendered([]byte("services:\n  firecrawl:\n    depends_on: [nuq-postgres]\n  nuq-postgres: {}\n  searxng: {}\n"))
	got, _ = r.DependencyOrder()
	if !slices.Equal(got, []string{"nuq-postgres", "firecrawl", "searxng"}) {
		t.Errorf("got %v", got)
	}

	// A cycle falls back to render order.
	r, _ = ParseRendered([]byte("services:\n  a:\n    depends_on: [b]\n  b:\n    depends_on: [a]\n  c: {}\n"))
	got, ok = r.DependencyOrder()
	if ok || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("cycle: got %v %v", got, ok)
	}
}

func TestPinVar(t *testing.T) {
	raw := `x-common: &common
  restart: unless-stopped
services:
  relay:
    <<: *common
    image: ghcr.io/block/buzz:main@${BUZZ_DIGEST}
  api:
    image: ghcr.io/firecrawl/firecrawl:${FIRECRAWL_TAG:-2.11.0}
  fixed:
    image: docker.io/redis:7
`
	for svc, want := range map[string]string{"relay": "BUZZ_DIGEST", "api": "FIRECRAWL_TAG"} {
		if got, err := PinVar([]byte(raw), svc); err != nil || got != want {
			t.Errorf("PinVar(%s) = %q, %v", svc, got, err)
		}
	}
	if _, err := PinVar([]byte(raw), "fixed"); err == nil {
		t.Error("expected an error for an image with no variable")
	}
}
