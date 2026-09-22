// Package apply implements check, apply and apply-all on top of the
// registry, Incus, incus-compose and git adapters.
package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/OleMussmann/incus-compose-update/internal/compose"
	"github.com/OleMussmann/incus-compose-update/internal/gitsync"
	"github.com/OleMussmann/incus-compose-update/internal/imageref"
	"github.com/OleMussmann/incus-compose-update/internal/incus"
	"github.com/OleMussmann/incus-compose-update/internal/registry"
	"github.com/OleMussmann/incus-compose-update/internal/stack"
	"github.com/OleMussmann/incus-compose-update/internal/version"
)

// ErrReported means the failure has already been explained on the output;
// the caller only has to exit non-zero.
var ErrReported = errors.New("failure already reported")

// Settings are the environment-driven knobs.
type Settings struct {
	Pool            string        // INCUS_POOL
	Project         string        // INCUS_PROJECT; empty means the rendered name:
	ReensureTimeout string        // REENSURE_TIMEOUT, an incus-compose duration
	ReadyTries      int           // READY_TRIES
	ReadyInterval   time.Duration // READY_INTERVAL
	// DryRun runs every read-only step and reports what would change,
	// without touching the checkout, the pins or the server.
	DryRun bool
}

// App wires the commands to their dependencies.
type App struct {
	Settings
	Dirs     []string
	Compose  compose.Compose
	Incus    incus.Client
	Git      gitsync.Git
	Registry registry.Registry

	Out, Err io.Writer
	In       io.Reader
	// Interactive reports whether In is a terminal a human can answer on.
	Interactive bool
	Now         func() time.Time
	Sleep       func(time.Duration)
}

func (a *App) say(format string, args ...any)  { fmt.Fprintf(a.Out, format+"\n", args...) }
func (a *App) warn(format string, args ...any) { fmt.Fprintf(a.Err, format+"\n", args...) }

// syncIn freshens a checkout, or in a dry run only reports if it is stale.
func (a *App) syncIn(dir string) error {
	if a.DryRun {
		a.Git.WarnIfStale(dir)
		return nil
	}
	return a.Git.SyncIn(dir)
}

// stackDirs yields the configured dirs that hold a compose.yaml.
func (a *App) stackDirs() []string {
	var out []string
	for _, d := range a.Dirs {
		if _, err := os.Stat(filepath.Join(d, "compose.yaml")); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// render returns the stack's rendered definition. versions.env is created if
// missing, since incus-compose errors on a listed env file that is absent.
func (a *App) render(dir string) (*stack.Rendered, error) {
	env := filepath.Join(dir, "versions.env")
	if _, err := os.Stat(env); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(env, nil, 0o644); err != nil {
			return nil, err
		}
	}
	out, err := a.Compose.Config(dir)
	if err != nil {
		return nil, fmt.Errorf("incus-compose config failed in %s", dir)
	}
	return stack.ParseRendered(out)
}

// target is one managed service in one stack, with everything derived from
// its definition.
type target struct {
	dir     string
	envFile string
	service string
	update  *stack.Update
	ref     imageref.Ref
	pinVar  string
	project string
}

func (a *App) target(dir string, r *stack.Rendered, service string) (*target, error) {
	s := r.Service(service)
	if s == nil {
		return nil, fmt.Errorf("service '%s' not found in %s", service, dir)
	}
	u, err := s.Update()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		return nil, err
	}
	pinVar, err := stack.PinVar(raw, service)
	if err != nil {
		return nil, err
	}
	project := a.Project
	if project == "" {
		project = r.Name
	}
	return &target{
		dir:     dir,
		envFile: filepath.Join(dir, "versions.env"),
		service: service,
		update:  u,
		ref:     imageref.Parse(s.Image),
		pinVar:  pinVar,
		project: project,
	}, nil
}

// managing returns the stack dirs in which service is defined and managed.
// Service names are only unique per stack: two stacks may both define
// `redis` with only one managing it, so a stack that merely defines the name
// does not count.
func (a *App) managing(service string) ([]string, error) {
	var managed, unmanaged []string
	for _, dir := range a.stackDirs() {
		r, err := a.render(dir)
		if err != nil {
			return nil, err
		}
		switch s := r.Service(service); {
		case s == nil:
		case s.Managed:
			managed = append(managed, dir)
		default:
			unmanaged = append(unmanaged, dir)
		}
	}
	if len(managed) == 0 {
		if len(unmanaged) > 0 {
			return nil, fmt.Errorf("service '%s' has no x-update: block (in %v)", service, unmanaged)
		}
		return nil, fmt.Errorf("service '%s' not found in any stack dir", service)
	}
	return managed, nil
}

// latest resolves the newest upstream ref for t. Shared by check and
// apply-all so the two cannot disagree about what "latest" means.
func (a *App) latest(ctx context.Context, t *target, cur string) (string, error) {
	u, ref := t.update, t.ref
	if u.Mode == "digest" {
		// The tag from the image ref, never a hardcoded `latest`: the label in
		// image: and the pinned digest must describe the same image.
		d, err := a.Registry.Digest(ctx, ref.Host, ref.Repo, ref.Tag)
		if err != nil {
			return "", fmt.Errorf("could not fetch digest for %s:%s: %w", ref.Repo, ref.Tag, err)
		}
		return d, nil
	}

	var tags []string
	var err error
	if u.Order == version.OrderPushed {
		tags, err = a.Registry.PushedTags(ctx, ref.Host, ref.Repo, u.TagRE.MatchString)
	} else {
		tags, err = a.Registry.Tags(ctx, ref.Host, ref.Repo)
	}
	if err != nil {
		return "", fmt.Errorf("could not list tags for %s/%s: %w", ref.Host, ref.Repo, err)
	}

	next, ok := version.Newest(tags, u.TagRE, u.Order)
	if !ok {
		a.warn("no tags matched %s", u.TagRE)
		a.warn("upstream may have changed its versioning scheme. Saw:")
		for i, tag := range tags {
			if i == 8 {
				break
			}
			a.warn("  %s", tag)
		}
		return "", fmt.Errorf("update tag_re for %s in compose.yaml", t.service)
	}
	if err := version.Guard(cur, next, u.Order); err != nil {
		return "", err
	}
	return next, nil
}
