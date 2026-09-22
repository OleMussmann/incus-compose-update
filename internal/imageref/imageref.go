// Package imageref splits an image reference into registry host, repository,
// tag and digest, spelled the way Incus and incus-compose record them.
//
// It deliberately does not use go-containerregistry's name.ParseReference:
// that normalises docker.io to index.docker.io, while identity checks compare
// against image.id / user.image_alias, which use docker.io.
package imageref

import "strings"

// Ref is a parsed image reference.
type Ref struct {
	Host   string // registry host, e.g. docker.io, ghcr.io, registry.local:5000
	Repo   string // repository path; Docker Hub official images get library/
	Tag    string // tag; "latest" when the reference names none
	Digest string // digest after '@', if any
}

// Parse parses ref.
//
// Order matters: strip the digest, then the tag, and only then split off the
// host. Cutting at the first ':' instead would lose the tag of a ref that
// also carries a digest, and would read a registry port as a tag.
func Parse(ref string) Ref {
	r := Ref{Tag: "latest"}

	// Digest first — everything after '@' is opaque.
	name := ref
	if i := strings.Index(ref, "@"); i >= 0 {
		name, r.Digest = ref[:i], ref[i+1:]
	}

	// Then the tag: the part after the LAST ':', but only when it holds no
	// '/'. In `host:5000/repo` that colon introduces a port, not a tag.
	if i := strings.LastIndex(name, ":"); i >= 0 && !strings.Contains(name[i+1:], "/") {
		r.Tag = name[i+1:]
		name = name[:i]
	}

	// The first path segment is a registry host when it looks like one: a dot
	// (registry.example.com), a port (registry:5000), or literally localhost.
	// Anything else is a Docker Hub namespace.
	first, rest, hasSlash := strings.Cut(name, "/")
	if hasSlash && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		r.Host, r.Repo = first, rest
	} else {
		r.Host, r.Repo = "docker.io", name
	}

	// Docker Hub namespaces single-segment official images under library/.
	if r.Host == "docker.io" && !strings.Contains(r.Repo, "/") {
		r.Repo = "library/" + r.Repo
	}
	return r
}

// IsDigest reports whether a pin is a digest (algorithm:hex) rather than a tag.
func IsDigest(pin string) bool {
	algo, hex, ok := strings.Cut(pin, ":")
	if !ok || algo == "" || len(hex) < 32 {
		return false
	}
	for _, c := range hex {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Join appends a pin to a repository: '@' for a digest, ':' for a tag.
func Join(repo, pin string) string {
	if IsDigest(pin) {
		return repo + "@" + pin
	}
	return repo + ":" + pin
}

// PinOf is the inverse of Join on an image alias: the digest after '@', or
// else the tag after the last ':' of the final path segment.
func PinOf(alias string) string {
	if i := strings.Index(alias, "@"); i >= 0 {
		return alias[i+1:]
	}
	if i := strings.LastIndex(alias, ":"); i >= 0 && !strings.Contains(alias[i+1:], "/") {
		return alias[i+1:]
	}
	return ""
}
