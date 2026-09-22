package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// pagingRegistry wraps ggcr's in-memory registry, caps tags/list at 2 entries
// per page and adds the `Link: <...>; rel="next"` header real registries send
// (the in-memory one pages on n/last but never links), so pagination is
// exercised end to end.
func pagingRegistry(t *testing.T) (host string, listCalls *int) {
	reg := ggcrregistry.New()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tags/list") {
			reg.ServeHTTP(w, r)
			return
		}
		calls++
		q := r.URL.Query()
		q.Set("n", "2")
		r.URL.RawQuery = q.Encode()
		rec := httptest.NewRecorder()
		reg.ServeHTTP(rec, r)
		var page struct {
			Tags []string `json:"tags"`
		}
		json.Unmarshal(rec.Body.Bytes(), &page)
		if len(page.Tags) == 2 {
			w.Header().Set("Link", fmt.Sprintf(`<%s?n=2&last=%s>; rel="next"`, r.URL.Path, page.Tags[1]))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &calls
}

func TestTagsPaginatesAndDigestResolves(t *testing.T) {
	host, listCalls := pagingRegistry(t)
	ctx := context.Background()

	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 5 {
		tag := fmt.Sprintf("1.0.%d", i)
		want = append(want, tag)
		ref, _ := name.NewTag(host+"/team/app:"+tag, name.Insecure)
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
	}

	c := &Client{Insecure: true}
	got, err := c.Tags(ctx, host, "team/app")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
	if *listCalls < 3 {
		t.Errorf("5 tags at 2 per page took %d tags/list calls; pagination not followed", *listCalls)
	}

	// A multi-arch image resolves to its index digest, not a platform's.
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})
	ref, _ := name.NewTag(host+"/team/multi:main", name.Insecure)
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	wantDigest, _ := idx.Digest()
	d, err := c.Digest(ctx, host, "team/multi", "main")
	if err != nil || d != wantDigest.String() {
		t.Errorf("Digest = %q, %v; want %s", d, err, wantDigest)
	}

	if _, err := c.Digest(ctx, host, "team/absent", "main"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing repo error = %v", err)
	}
}

func TestPushedTags(t *testing.T) {
	pages := map[string]string{
		"1": `{"next": "NEXT/v2/repositories/searxng/searxng/tags?page=2", "results": [{"name": "latest"}, {"name": "2026.9.22-aaa"}]}`,
		"2": `{"next": "", "results": [{"name": "2026.9.21-019460e07"}, {"name": "2026.9.20-111111111"}]}`,
	}
	var hub *httptest.Server
	hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/repositories/searxng/searxng/tags") {
			http.NotFound(w, r)
			return
		}
		p := r.URL.Query().Get("page")
		if p == "" {
			p = "1"
		}
		fmt.Fprint(w, strings.ReplaceAll(pages[p], "NEXT", hub.URL))
	}))
	defer hub.Close()

	c := &Client{HubURL: hub.URL}
	ctx := context.Background()
	daily := func(s string) bool { return len(s) == 19 }

	got, err := c.PushedTags(ctx, "docker.io", "searxng/searxng", daily)
	if err != nil {
		t.Fatal(err)
	}
	// Stops after page 2, the first page holding a wanted tag.
	if !slices.Equal(got, []string{"latest", "2026.9.22-aaa", "2026.9.21-019460e07", "2026.9.20-111111111"}) {
		t.Errorf("got %v", got)
	}

	if _, err := c.PushedTags(ctx, "ghcr.io", "block/buzz", daily); !errors.Is(err, ErrNoPushOrder) {
		t.Errorf("non-Hub registry: %v", err)
	}
	if _, err := c.PushedTags(ctx, "docker.io", "minio/minio", daily); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing Hub repo: %v", err)
	}
}
