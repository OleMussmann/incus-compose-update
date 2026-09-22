// Package registry lists tags and resolves digests on container registries.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Registry is what update resolution needs from a registry.
type Registry interface {
	// Tags lists every tag of host/repo, in no particular order.
	Tags(ctx context.Context, host, repo string) ([]string, error)
	// PushedTags lists tags newest-pushed first, stopping once a page
	// contains a tag for which want returns true.
	PushedTags(ctx context.Context, host, repo string, want func(string) bool) ([]string, error)
	// Digest resolves host/repo:tag to the digest a pull of it would get —
	// the index digest for a multi-arch image.
	Digest(ctx context.Context, host, repo, tag string) (string, error)
}

// ErrNoPushOrder is returned by PushedTags for registries whose API carries no
// push time. The OCI distribution tags API returns names only; of the
// registries in use, only Docker Hub's own API orders tags by push time.
var ErrNoPushOrder = errors.New("this registry does not report push order; `order: pushed` only works on docker.io — use `mode: digest` instead")

// Client is the Registry backed by the network.
type Client struct {
	// Insecure talks plain HTTP (tests).
	Insecure bool
	// HubURL is the Docker Hub API base; empty means https://hub.docker.com.
	HubURL string
	// HubMaxPages bounds PushedTags; zero means 10 pages of 100 tags.
	HubMaxPages int
	HTTP        *http.Client
}

func (c *Client) nameOpts() []name.Option {
	if c.Insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}

func (c *Client) remoteOpts(ctx context.Context) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain)}
	if c.HTTP != nil {
		opts = append(opts, remote.WithTransport(c.HTTP.Transport))
	}
	return opts
}

// Tags implements Registry. remote.List follows Link pagination to the end.
func (c *Client) Tags(ctx context.Context, host, repo string) ([]string, error) {
	r, err := name.NewRepository(host+"/"+repo, c.nameOpts()...)
	if err != nil {
		return nil, err
	}
	tags, err := remote.List(r, c.remoteOpts(ctx)...)
	if err != nil {
		return nil, describe(err, host+"/"+repo)
	}
	return tags, nil
}

// Digest implements Registry.
func (c *Client) Digest(ctx context.Context, host, repo, tag string) (string, error) {
	ref, err := name.NewTag(host+"/"+repo+":"+tag, c.nameOpts()...)
	if err != nil {
		return "", err
	}
	desc, err := remote.Head(ref, c.remoteOpts(ctx)...)
	if err != nil {
		return "", describe(err, host+"/"+repo+":"+tag)
	}
	return desc.Digest.String(), nil
}

// PushedTags implements Registry using Docker Hub's API, which can order by
// last_updated. Other registries return ErrNoPushOrder.
func (c *Client) PushedTags(ctx context.Context, host, repo string, want func(string) bool) ([]string, error) {
	if host != "docker.io" {
		return nil, ErrNoPushOrder
	}
	base := c.HubURL
	if base == "" {
		base = "https://hub.docker.com"
	}
	pages := c.HubMaxPages
	if pages == 0 {
		pages = 10
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}

	next := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=100&ordering=last_updated", base, repo)
	var tags []string
	for page := 0; next != "" && page < pages; page++ {
		var body struct {
			Next    string `json:"next"`
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := getJSON(ctx, hc, next, &body); err != nil {
			return nil, fmt.Errorf("docker.io/%s: %w", repo, err)
		}
		hit := false
		for _, r := range body.Results {
			tags = append(tags, r.Name)
			hit = hit || want(r.Name)
		}
		if hit {
			break
		}
		next = body.Next
	}
	return tags, nil
}

func getJSON(ctx context.Context, hc *http.Client, u string, v any) error {
	if _, err := url.Parse(u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("repository not found on Docker Hub")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the Docker Hub API returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// describe turns a registry "not found" or "unauthorized" into one message.
// Registries answer an anonymous request for a repository that does not
// exist with either, so the two cannot be told apart.
func describe(err error, what string) error {
	var te *transport.Error
	if errors.As(err, &te) && (te.StatusCode == http.StatusNotFound || te.StatusCode == http.StatusUnauthorized) {
		return fmt.Errorf("%s: not found, or requires authentication (HTTP %d)", what, te.StatusCode)
	}
	return fmt.Errorf("%s: %w", what, err)
}
