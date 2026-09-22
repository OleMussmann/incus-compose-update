package apply

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OleMussmann/incus-compose-update/internal/pins"
)

type planned struct {
	dir, service, cur, next string
}

// ApplyAll applies every pending update, one at a time.
//
// Every target ref is resolved before anything is mutated, so an unreachable
// registry or a stale tag_re fails with all stacks on their current pins
// rather than halfway through. Applies run strictly in sequence: services in
// a stack share one versions.env and one git index, and recreates start
// shared dependencies.
func (a *App) ApplyAll(ctx context.Context, assumeYes bool) error {
	var plan []planned
	for _, dir := range a.stackDirs() {
		// Sync before planning, so the plan is built from pins that match the
		// server; Apply's own sync of the same dir is then a no-op.
		if err := a.syncIn(dir); err != nil {
			return err
		}
		r, err := a.render(dir)
		if err != nil {
			return err
		}
		order, ok := r.DependencyOrder()
		if !ok {
			a.warn("!! %s: could not order services by dependency (cycle?),", dir)
			a.warn("   falling back to render order")
		}
		for _, name := range order {
			if !r.Service(name).Managed {
				continue
			}
			t, err := a.target(dir, r, name)
			if err != nil {
				return err
			}
			cur, err := pins.Read(t.envFile, t.pinVar)
			if err != nil {
				return err
			}
			if cur == "" {
				continue // no pin to move; check reports these as skipped
			}
			next, err := a.latest(ctx, t, cur)
			if err != nil {
				return err
			}
			if next != cur {
				plan = append(plan, planned{dir, name, cur, next})
			}
		}
	}

	if len(plan) == 0 {
		a.say("everything is up to date")
		return nil
	}

	a.say("planned updates:")
	last := ""
	for _, p := range plan {
		if p.dir != last {
			a.say("")
			a.say("  stack: %s", p.dir)
			last = p.dir
		}
		a.say("    %-22s %s -> %s", p.service, p.cur, p.next)
	}
	a.say("")

	if a.DryRun {
		a.say("dry run — nothing was changed")
		return nil
	}
	if !assumeYes {
		if !a.Interactive {
			return errors.New("apply-all needs a terminal to confirm — pass --yes for unattended runs")
		}
		fmt.Fprintf(a.Out, "recreate %d service(s), one at a time? [y/N] ", len(plan))
		reply, _ := bufio.NewReader(a.In).ReadString('\n')
		switch strings.TrimSpace(reply) {
		case "y", "Y", "yes", "YES":
		default:
			a.say("aborted — nothing was changed")
			return nil
		}
		a.say("")
	}

	// Apply re-renders and re-reads the pin for itself rather than trusting
	// the plan, so a stale plan can never commit a wrong pin.
	for n, p := range plan {
		a.say("==================================================================")
		a.say("[%d/%d] %s: %s -> %s", n+1, len(plan), p.service, p.cur, p.next)
		a.say("==================================================================")
		if err := a.Apply(ctx, p.service, p.next, p.dir); err != nil {
			if !errors.Is(err, ErrReported) {
				a.warn("error: %v", err)
			}
			a.say("")
			a.say("!! stopped at %s — %d of %d applied", p.service, n, len(plan))
			a.say("   the remaining %d service(s) are untouched and still", len(plan)-n-1)
			a.say("   on their current pins. Fix this one, then re-run apply-all.")
			return ErrReported
		}
		a.say("")
	}
	a.say("applied %d update(s)", len(plan))
	return nil
}
