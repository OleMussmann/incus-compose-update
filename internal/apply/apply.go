package apply

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OleMussmann/incus-compose-update/internal/imageref"
	"github.com/OleMussmann/incus-compose-update/internal/pins"
)

// Apply moves one service to ref: snapshot, pin, recreate, re-ensure the
// stack, verify, commit, push. dir names the stack when the caller already
// knows it (apply-all); empty means look the service up by name.
func (a *App) Apply(ctx context.Context, service, ref, dir string) error {
	if service == "" || ref == "" {
		return fmt.Errorf("usage: incus-compose-update apply <service> <ref>")
	}
	if dir == "" {
		dirs, err := a.managing(service)
		if err != nil {
			return err
		}
		if len(dirs) > 1 {
			return fmt.Errorf("service '%s' is managed in several stacks (%s) — pick one with STACK_DIRS=<dir>", service, strings.Join(dirs, " "))
		}
		dir = dirs[0]
	}

	// Freshen the checkout before any pin is read: a pull moves versions.env.
	if err := a.syncIn(dir); err != nil {
		return err
	}
	r, err := a.render(dir)
	if err != nil {
		return err
	}
	t, err := a.target(dir, r, service)
	if err != nil {
		return err
	}
	cur, err := pins.Read(t.envFile, t.pinVar)
	if err != nil {
		return err
	}
	if cur == "" {
		return fmt.Errorf("no %s in %s", t.pinVar, t.envFile)
	}
	instance := service + "-1"

	// versions.env is the record, not the server: an apply that pinned and
	// then failed or was cut short leaves it ahead of the instance. "Already
	// on" therefore has to be confirmed against what actually runs.
	if cur == ref {
		id := a.identity(t, instance, ref)
		if id.status != mismatch {
			a.say("already on %s", ref)
			return nil
		}
		from := imageref.PinOf(id.gotAlias)
		if from == "" {
			a.say("!! versions.env already pins %s at %s, but %s runs:", service, ref, instance)
			a.say("     %s", id.got())
			a.say("   and records no image alias to read its current pin from.")
			a.say("   inspect: incus --project %s config show %s", t.project, instance)
			return ErrReported
		}
		a.say("==> note: versions.env already pins %s, but %s still runs %s.", ref, instance, from)
		a.say("    An earlier apply did not finish; applying from %s.", from)
		cur = from
	}

	if err := a.preflight(t); err != nil {
		return err
	}

	// Is the pin about to move the one actually running? If not, this
	// checkout does not describe the server: the commit would record a "from"
	// version that was never deployed. Stop while nothing has been touched.
	live := a.identity(t, instance, cur)
	if live.status == mismatch {
		// Applying what the instance already runs moves the record towards
		// reality. That is the rollback printed when a recreate fails to take,
		// so it must stay possible.
		if a.identity(t, instance, ref).status == match {
			a.say("==> note: versions.env pins %s, but %s already runs %s.", cur, instance, ref)
			a.say("    Applying it brings the record back in line with the server.")
		} else {
			a.say("!! %s does not match the server", dir)
			a.say("   versions.env pins %s at: %s", service, cur)
			a.say("   %s is actually running:  %s", instance, live.got())
			a.say("")
			a.say("   Either another checkout updated this service, or the server was")
			a.say("   changed outside this tool. Applying from here would record a")
			a.say("   change that never happened, and this same tree is what a plain")
			a.say("   'incus-compose up' would deploy from.")
			a.say("   fix: git -C %s pull --rebase", dir)
			if pin := imageref.PinOf(live.gotAlias); pin != "" {
				a.say("   or, to adopt what the server already runs:")
				a.say("        incus-compose-update apply %s %s", service, pin)
			}
			return ErrReported
		}
	}
	if live.status == unknown {
		a.say("==> note: %s records no image identity, so the current pin", instance)
		a.say("    cannot be confirmed against the server. Proceeding.")
	}

	if a.DryRun {
		if t.update.Volume != "none" {
			a.say("--> would snapshot %s:%s/%s as %s", t.project, a.Pool, t.update.Volume, snapshotName(ref, a.Now()))
		}
		a.say("--> would pin %s: %s -> %s in %s", t.pinVar, cur, ref, t.envFile)
		a.say("--> would recreate %s, start it, and re-ensure the stack", service)
		a.say("--> would verify %s is on %s and answers: %s", instance, imageref.Join(t.ref.Repo, ref), strings.Join(t.update.VersionCmd, " "))
		a.say("--> would commit \"%s: %s -> %s\" and push", service, cur, ref)
		a.say("dry run — nothing was changed")
		return nil
	}

	before := a.Compose.Exec(dir, service, t.update.VersionCmd)

	snap := ""
	if t.update.Volume != "none" {
		snap = snapshotName(ref, a.Now())
		a.say("==> snapshotting %s:%s/%s as %s", t.project, a.Pool, t.update.Volume, snap)
		if err := a.Incus.Snapshot(t.project, a.Pool, t.update.Volume, snap); err != nil {
			a.say("!! could not snapshot %s — nothing was changed", t.update.Volume)
			return err
		}
	}

	a.say("==> pinning %s -> %s", cur, ref)
	if err := pins.Write(t.envFile, t.pinVar, ref); err != nil {
		a.say("!! could not write %s to %s", t.pinVar, t.envFile)
		return err
	}

	statesBefore, err := a.states(t.project)
	if err != nil {
		return err
	}

	a.say("==> pulling and recreating")
	// A failure here is not returned on directly: the instance may be half
	// rebuilt, and the checks below are what can tell — and print the rollback.
	// The one case settled here is a recreate that failed before replacing
	// anything (a failed pull aborts it ahead of the delete): the pin goes
	// back, so the record matches the server and a re-run is not mistaken for
	// "already on". The re-ensure below still runs, on the restored pin, to
	// bring back any dependents the recreate removed.
	recreateErr := a.Compose.Recreate(dir, service)
	untouched := false
	if recreateErr != nil {
		if a.identity(t, instance, cur).status == match {
			untouched = true
			a.say("    !! recreate exited non-zero and %s still runs %s — restoring the pin", instance, cur)
			if err := pins.Write(t.envFile, t.pinVar, cur); err != nil {
				a.say("!! could not restore %s to %s in %s", t.pinVar, cur, t.envFile)
				return err
			}
		} else {
			a.say("    !! recreate exited non-zero — continuing to verify")
		}
	}

	// A service-scoped `up` can create the instance and leave it stopped: its
	// "Starting resources" phase walks the whole project graph and bails on
	// any resource the run did not ensure, including sibling services'
	// images. `start` walks a smaller graph; on a running service it is a
	// no-op, and a start that genuinely fails shows up in the readiness check.
	_ = a.Compose.Start(dir, service)

	// Re-ensure the whole stack. A service-scoped recreate deletes the
	// target's dependents to rebuild the target and does not bring them back.
	// A plain stack-wide up rebuilds exactly those and is a no-op for anything
	// running: no recreate, so nothing healthy is deleted; no pull, so no
	// sibling moves off its own pin.
	a.say("==> re-ensuring stack")
	if err := a.Compose.Reensure(dir, a.ReensureTimeout); err != nil {
		a.say("    !! re-ensure did not finish cleanly — see what is missing below")
	}

	// Whatever the re-ensure managed, nothing may have vanished across the
	// apply, and nothing that was running may be down. Instances already
	// stopped beforehand are not this apply's doing.
	statesAfter, err := a.states(t.project)
	if err != nil {
		return err
	}
	var vanished, stopped []string
	for _, name := range sortedKeys(statesBefore) {
		b := statesBefore[name]
		after, ok := statesAfter[name]
		switch {
		case !ok:
			vanished = append(vanished, name)
		case b.running && !after.running:
			stopped = append(stopped, fmt.Sprintf("%s (%s)", name, after.state))
		}
	}
	if len(vanished) > 0 || len(stopped) > 0 {
		a.say("")
		if len(vanished) > 0 {
			a.say("!! instances that existed before this apply are gone after it:")
			for _, v := range vanished {
				a.say("     %s", v)
			}
		}
		if len(stopped) > 0 {
			a.say("!! instances that were running before this apply are down after it:")
			for _, s := range stopped {
				a.say("     %s", s)
			}
		}
		a.say("   Recreating %s left them that way and the re-ensure did not", service)
		a.say("   recover them, so the stack is incomplete. Stopping before anything")
		a.say("   else is touched — the re-ensure output above says why.")
		a.say("   inspect: incus --project %s list", t.project)
		a.say("   rebuild: ( cd %s && incus-compose --env-file .env --env-file versions.env up -d )", dir)
		return ErrReported
	}

	if untouched {
		a.say("")
		a.say("!! recreating %s failed — the incus-compose output above says why", service)
		a.say("   %s was not replaced and still runs %s, and %s is back on it.", instance, cur, t.pinVar)
		a.say("   Nothing needs rolling back. Once the cause is fixed, re-run:")
		a.say("     incus-compose-update apply %s %s", service, ref)
		if snap != "" {
			a.say("   the snapshot %s of %s is kept.", snap, t.update.Volume)
		}
		return ErrReported
	}

	// Two independent facts, checked separately because neither implies the
	// other: the instance was rebuilt on the new ref, and it came back up.
	// Image identity settles the first; the version probe only shows the
	// container answers, since a rebuild of a digest-pinned base image can
	// legitimately report the same version.
	a.say("==> waiting for readiness")
	after, ready := "", false
	for range a.ReadyTries {
		a.Sleep(a.ReadyInterval)
		if after = a.Compose.Exec(dir, service, t.update.VersionCmd); after != "" {
			ready = true
			break
		}
		fmt.Fprint(a.Out, ".")
	}
	a.say("")

	_ = a.Compose.Ps(dir)

	rollback := func() {
		a.say("   roll back:  incus-compose-update apply %s %s", service, cur)
		if snap != "" {
			a.say("   or restore: incus --project %s storage volume restore %s %s %s", t.project, a.Pool, t.update.Volume, snap)
		}
	}

	id := a.identity(t, instance, ref)
	switch id.status {
	case unknown:
		a.say("")
		a.say("!! cannot read image identity of %s:%s", t.project, instance)
		a.say("   expected image.id: %s", id.wantID)
		a.say("   neither image.id nor user.image_alias is set — wrong instance name,")
		a.say("   or this Incus/incus-compose no longer records it.")
		a.say("   inspect:    incus --project %s config show %s", t.project, instance)
		rollback()
		return ErrReported
	case mismatch:
		want, got := id.pair()
		a.say("")
		a.say("!! %s is not running the new image", instance)
		a.say("   wanted: %s", want)
		a.say("   got:    %s", got)
		if recreateErr != nil {
			a.say("   the recreate exited non-zero and left the instance on neither pin.")
		} else {
			a.say("   the recreate reported success, but the instance was not rebuilt on the new image.")
		}
		a.say("   logs:       incus --project %s console %s --show-log", t.project, instance)
		rollback()
		return ErrReported
	}
	if !ready {
		a.say("")
		a.say("!! %s is on the new image but did not respond in %.0fs", instance, (time.Duration(a.ReadyTries) * a.ReadyInterval).Seconds())
		a.say("   image:  %s", id.wantID)
		a.say("   probe:  %s", strings.Join(t.update.VersionCmd, " "))
		a.say("   it produced no output — the container may still be booting, may be")
		a.say("   crash-looping, or the probe itself may be wrong for this image.")
		a.say("   logs:       incus --project %s console %s --show-log", t.project, instance)
		rollback()
		return ErrReported
	}

	a.say("==> verified %s", id.wantID)
	if after == before {
		a.say("    %s (unchanged — rebuild of the same release)", after)
	} else {
		if before == "" {
			before = "<none>"
		}
		a.say("    %s", before)
		a.say(" -> %s", after)
	}

	// The checkouts describe one shared server, so an unpushed pin leaves
	// every other checkout believing something false about it.
	if a.Git.Commit(dir, "versions.env", fmt.Sprintf("%s: %s -> %s", service, cur, ref)) {
		a.say("==> pinned and committed")
		a.Git.SyncOut(dir)
	} else {
		a.say("==> pinned (not committed — no repo, or nothing to commit)")
	}
	a.say("==> done. rollback: incus-compose-update apply %s %s", service, cur)
	return nil
}

func (a *App) preflight(t *target) error {
	if t.update.Volume == "none" {
		return nil
	}
	ok, err := a.Incus.VolumeExists(t.project, a.Pool, t.update.Volume)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	a.warn("cannot find volume '%s' on pool '%s' in project '%s'", t.update.Volume, a.Pool, t.project)
	if vols, err := a.Incus.CustomVolumes(t.project, a.Pool); err == nil {
		a.warn("custom volumes there: %s", strings.Join(vols, " "))
	}
	return fmt.Errorf("set INCUS_POOL or x-update.volume to match")
}

type instanceState struct {
	state   string
	running bool
}

func (a *App) states(project string) (map[string]instanceState, error) {
	list, err := a.Incus.Instances(project)
	if err != nil {
		return nil, fmt.Errorf("list instances in project %s: %w", project, err)
	}
	out := map[string]instanceState{}
	for _, i := range list {
		out[i.Name] = instanceState{i.State, i.Running}
	}
	return out, nil
}

func sortedKeys(m map[string]instanceState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- image identity ---

const (
	match = iota
	mismatch
	unknown // the instance records no identity (or does not exist)
)

type identity struct {
	status                             int
	wantID, wantAlias, gotID, gotAlias string
}

func (i identity) got() string {
	if i.gotAlias != "" {
		return i.gotAlias
	}
	return i.gotID
}

// pair returns wanted and got spelled the same way: as aliases when the
// instance records one, else as image.id. The alias carries the tag; for an
// OCI image image.id is often a digest that a tag can never resemble.
func (i identity) pair() (want, got string) {
	if i.gotAlias != "" {
		return i.wantAlias, i.gotAlias
	}
	return i.wantID, i.gotID
}

// identity checks whether the instance was created from pin. Two keys record
// it and either settles it, so the check survives one of them changing shape:
//
//	image.id          set by Incus from the OCI descriptor, host-less:
//	                  searxng/searxng:2026.8.28-a30b2d474
//	user.image_alias  set by incus-compose, host-qualified:
//	                  docker.io/searxng/searxng:2026.8.28-a30b2d474
//
// Asked before an apply ("does the pin match reality?") and after ("did the
// recreate take?").
func (a *App) identity(t *target, instance, pin string) identity {
	id := identity{
		wantID:    imageref.Join(t.ref.Repo, pin),
		wantAlias: imageref.Join(t.ref.Host+"/"+t.ref.Repo, pin),
	}
	if inst, err := a.Incus.Instance(t.project, instance); err == nil {
		id.gotID, id.gotAlias = inst.Config["image.id"], inst.Config["user.image_alias"]
	}
	switch {
	case id.gotID == "" && id.gotAlias == "":
		id.status = unknown
	case id.gotID == id.wantID || id.gotAlias == id.wantAlias:
		id.status = match
	default:
		id.status = mismatch
	}
	return id
}

// --- snapshot names ---

var digestRE = regexp.MustCompile(`^([a-z0-9]+):([0-9a-f]{12})[0-9a-f]*$`)
var unsafeRE = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// snapshotName is pre-<ref>-<YYYYmmddHHMM>, within Incus's 64-character limit
// for snapshot names. A digest ref alone is 71 characters, so a digest is
// shortened to its algorithm and first 12 hex digits, the usual
// abbreviation; other characters outside [A-Za-z0-9._-] become '-', and a
// long tag is truncated to fit.
func snapshotName(ref string, now time.Time) string {
	stamp := now.Format("200601021504")
	label := ref
	if m := digestRE.FindStringSubmatch(ref); m != nil {
		label = m[1] + "-" + m[2]
	} else {
		label = unsafeRE.ReplaceAllString(ref, "-")
	}
	if max := 64 - len("pre-") - len("-") - len(stamp); len(label) > max {
		label = label[:max]
	}
	return "pre-" + label + "-" + stamp
}
