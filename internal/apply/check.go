package apply

import (
	"context"
	"fmt"

	"github.com/OleMussmann/incus-compose-update/internal/pins"
	"github.com/OleMussmann/incus-compose-update/internal/stack"
)

// Check compares pins against upstream for one service, or for every managed
// service. A service that cannot be checked is reported and skipped, so one
// unreachable registry does not hide the rest; Check then returns an error.
func (a *App) Check(ctx context.Context, service string) error {
	failed := 0
	if service != "" {
		dirs, err := a.managing(service)
		if err != nil {
			return err
		}
		for _, dir := range dirs {
			if len(dirs) > 1 {
				a.say("== stack: %s", dir)
			}
			r, err := a.render(dir)
			if err != nil {
				return err
			}
			a.Git.WarnIfStale(dir)
			if !a.checkOne(ctx, dir, r, service) {
				failed++
			}
		}
	} else {
		for _, dir := range a.stackDirs() {
			a.say("== stack: %s", dir)
			a.Git.WarnIfStale(dir)
			r, err := a.render(dir)
			if err != nil {
				a.say("!! %v", err)
				a.say("")
				failed++
				continue
			}
			for _, s := range r.Services {
				if s.Managed && !a.checkOne(ctx, dir, r, s.Name) {
					failed++
				}
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d service(s) could not be checked", failed)
	}
	return nil
}

func (a *App) checkOne(ctx context.Context, dir string, r *stack.Rendered, service string) bool {
	fail := func(err error) bool {
		a.say("!! %s: %v", service, err)
		a.say("")
		return false
	}
	t, err := a.target(dir, r, service)
	if err != nil {
		return fail(err)
	}
	cur, err := pins.Read(t.envFile, t.pinVar)
	if err != nil {
		return fail(err)
	}
	if cur == "" {
		a.say("== %s: no %s in %s — skipping", service, t.pinVar, t.envFile)
		return true
	}

	a.say("== %s", service)
	next, err := a.latest(ctx, t, cur)
	if err != nil {
		return fail(err)
	}
	a.say("current: %s", cur)
	a.say("latest:  %s", next)
	if cur == next {
		a.say("up to date")
	} else {
		a.say("apply:     incus-compose-update apply %s %s", service, next)
	}
	a.say("")
	return true
}
