// Package gitsync keeps a stack checkout in step with its origin.
//
// versions.env is a record of what one server runs, and several checkouts may
// describe that same server. Freshness is therefore a correctness property: a
// stale tree misreports the running version, writes a "from" version into its
// commit message that was never deployed, and can roll the server back on a
// plain `incus-compose up`. So a run pulls before reading pins and pushes
// after writing them, and a git failure never fails a deploy that worked.
//
// Git is run as a subprocess so it uses the user's own ssh config and agent.
package gitsync

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Git is what the updater needs from git.
type Git interface {
	// SyncIn rebases the checkout onto its upstream, at most once per dir.
	SyncIn(dir string) error
	// WarnIfStale reports, without changing anything, when dir is behind.
	WarnIfStale(dir string)
	// Commit commits file; false means no repo or nothing to commit.
	Commit(dir, file, message string) bool
	// SyncOut pushes, retrying once after a rebase. Never fails.
	SyncOut(dir string)
}

// Syncer is the Git backed by the git binary.
type Syncer struct {
	Out io.Writer
	// AutoPush is the AUTO_PUSH setting; anything but "1" commits only.
	AutoPush string
	synced   map[string]bool
}

func (s *Syncer) say(format string, a ...any) { fmt.Fprintf(s.Out, format+"\n", a...) }

func git(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

func ok(c *exec.Cmd) bool { return c.Run() == nil }

func output(c *exec.Cmd) (string, bool) {
	b, err := c.Output()
	return strings.TrimSpace(string(b)), err == nil
}

// net runs network git non-interactively: a credential prompt that hangs
// halfway through an update is worse than a fetch that fails and says so.
func net(dir string, args ...string) *exec.Cmd {
	c := git(dir, args...)
	ssh := os.Getenv("GIT_SSH_COMMAND")
	if ssh == "" {
		ssh = "ssh"
	}
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND="+ssh+" -o BatchMode=yes")
	return c
}

func isRepo(dir string) bool      { return ok(git(dir, "rev-parse", "--git-dir")) }
func hasUpstream(dir string) bool { return ok(git(dir, "rev-parse", "--abbrev-ref", "@{upstream}")) }

// SyncIn implements Git.
func (s *Syncer) SyncIn(dir string) error {
	if s.synced == nil {
		s.synced = map[string]bool{}
	}
	if s.synced[dir] {
		return nil
	}
	s.synced[dir] = true

	if !isRepo(dir) {
		return nil
	}
	if !hasUpstream(dir) {
		s.say("==> %s: no upstream branch — skipping pull", dir)
		return nil
	}
	if !ok(net(dir, "fetch", "--quiet")) {
		s.say("==> %s: fetch failed — working from the local tree as-is.", dir)
		s.say("    If the other checkout has pushed since, this tree is stale.")
		return nil
	}

	counts, good := output(git(dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"))
	if !good {
		return nil
	}
	ahead, behind, _ := strings.Cut(counts, "\t")
	if behind == "0" {
		return nil
	}

	// A dirty versions.env here is a pin written by an apply that then failed
	// before committing. Rebasing over it would either stash a pin that is
	// actually deployed or conflict on the very line about to be rewritten.
	if !ok(git(dir, "diff", "--quiet", "--", "versions.env")) {
		s.say("!! %s is %s commit(s) behind origin, and versions.env has", dir, behind)
		s.say("   uncommitted changes — most likely a pin left behind by a failed apply.")
		s.say("   Settle it by hand before updating:")
		s.say("     git -C %s diff versions.env", dir)
		return errors.New("refusing to rebase over an uncommitted pin")
	}

	s.say("==> %s: %s behind, %s ahead — rebasing onto origin", dir, behind, ahead)
	if !ok(git(dir, "rebase", "--quiet", "@{upstream}")) {
		_ = git(dir, "rebase", "--abort").Run()
		s.say("!! rebase onto origin conflicted in %s; it was aborted, so the", dir)
		s.say("   tree is untouched. Both checkouts moved the same pin. The server is")
		s.say("   the tiebreaker — whatever it runs now is the truth:")
		s.say("     git -C %s log --oneline 'HEAD..@{upstream}'", dir)
		return fmt.Errorf("cannot rebase %s onto origin", dir)
	}
	return nil
}

// WarnIfStale implements Git. `check` must not rewrite the tree just because
// someone asked what is out of date.
func (s *Syncer) WarnIfStale(dir string) {
	if !isRepo(dir) || !hasUpstream(dir) || !ok(net(dir, "fetch", "--quiet")) {
		return
	}
	behind, good := output(git(dir, "rev-list", "--count", "HEAD..@{upstream}"))
	if good && behind != "0" {
		s.say("!! %s is %s commit(s) behind origin — the pins below may", dir, behind)
		s.say("   not be what the server runs. Pull before trusting them:")
		s.say("     git -C %s pull --rebase", dir)
		s.say("")
	}
}

// Commit implements Git.
func (s *Syncer) Commit(dir, file, message string) bool {
	return ok(git(dir, "commit", "-q", "-m", message, "--", file))
}

// SyncOut implements Git. By the time it runs the deploy has succeeded and
// been verified, so a rejected push means the record lagged, not that the
// update failed.
func (s *Syncer) SyncOut(dir string) {
	if s.AutoPush != "1" {
		s.say("    not pushed (AUTO_PUSH=%s): git -C %s push", s.AutoPush, dir)
		return
	}
	if !hasUpstream(dir) {
		s.say("    not pushed — no upstream branch configured")
		return
	}
	if ok(net(dir, "push", "--quiet")) {
		s.say("    pushed")
		return
	}
	// A rejection means the other checkout pushed between our sync and now.
	// One rebase-and-retry covers that race; anything else is for a human.
	if ok(net(dir, "fetch", "--quiet")) && ok(git(dir, "rebase", "--quiet", "@{upstream}")) && ok(net(dir, "push", "--quiet")) {
		s.say("    pushed (after rebasing onto a concurrent update)")
		return
	}
	_ = git(dir, "rebase", "--abort").Run()
	s.say("    !! push failed — the pin is committed here but not on origin.")
	s.say("       The server is updated and verified; this is a git problem only.")
	s.say("       retry: git -C %s push", dir)
}
