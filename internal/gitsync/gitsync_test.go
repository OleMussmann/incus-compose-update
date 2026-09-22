package gitsync

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// checkouts creates a bare origin and two clones of it, a and b, each holding
// a committed versions.env.
func checkouts(t *testing.T) (a, b string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	} {
		t.Setenv(k, v)
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	run(t, root, "clone", "-q", origin, seed)
	write(t, seed, "versions.env", pins("1", "1"))
	run(t, seed, "add", "versions.env")
	run(t, seed, "commit", "-q", "-m", "seed")
	run(t, seed, "push", "-q", "origin", "HEAD:main")
	a, b = filepath.Join(root, "a"), filepath.Join(root, "b")
	run(t, root, "clone", "-q", origin, a)
	run(t, root, "clone", "-q", origin, b)
	return a, b
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pins renders a versions.env with comment lines between the two pins, as
// real files have; git treats changes to adjacent lines as a conflict.
func pins(a, b string) string {
	return "A_TAG=" + a + "\n\n# B\n\nB_TAG=" + b + "\n"
}

func write(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pin(t *testing.T, dir, content, msg string) {
	t.Helper()
	write(t, dir, "versions.env", content)
	run(t, dir, "commit", "-q", "-am", msg)
}

func read(t *testing.T, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "versions.env"))
	return string(b)
}

func TestSyncInRebasesOntoOtherCheckout(t *testing.T) {
	a, b := checkouts(t)
	pin(t, b, pins("1", "2"), "b moves B")
	run(t, b, "push", "-q")

	var out bytes.Buffer
	s := &Syncer{Out: &out, AutoPush: "1"}
	if err := s.SyncIn(a); err != nil {
		t.Fatal(err)
	}
	if read(t, a) != pins("1", "2") {
		t.Errorf("a not rebased: %q", read(t, a))
	}
	if !strings.Contains(out.String(), "1 behind, 0 ahead — rebasing onto origin") {
		t.Errorf("output: %s", out.String())
	}

	// Once per dir: a second call does nothing, even with origin moved again.
	pin(t, b, pins("1", "3"), "b moves B again")
	run(t, b, "push", "-q")
	if err := s.SyncIn(a); err != nil || strings.Contains(read(t, a), "B_TAG=3") {
		t.Errorf("second SyncIn acted: %v %q", err, read(t, a))
	}
}

func TestSyncInRefusesDirtyPinWhenBehind(t *testing.T) {
	a, b := checkouts(t)
	pin(t, b, pins("1", "2"), "b moves B")
	run(t, b, "push", "-q")
	write(t, a, "versions.env", pins("9", "1"))

	var out bytes.Buffer
	err := (&Syncer{Out: &out}).SyncIn(a)
	if err == nil || err.Error() != "refusing to rebase over an uncommitted pin" {
		t.Fatalf("err = %v", err)
	}
	if read(t, a) != pins("9", "1") {
		t.Error("dirty pin was touched")
	}
}

func TestSyncInAbortsConflictingRebase(t *testing.T) {
	a, b := checkouts(t)
	pin(t, b, pins("2", "1"), "b moves A")
	run(t, b, "push", "-q")
	pin(t, a, pins("3", "1"), "a moves A")
	head := run(t, a, "rev-parse", "HEAD")

	var out bytes.Buffer
	err := (&Syncer{Out: &out}).SyncIn(a)
	if err == nil || !strings.Contains(err.Error(), "cannot rebase") {
		t.Fatalf("err = %v", err)
	}
	if run(t, a, "rev-parse", "HEAD") != head || read(t, a) != pins("3", "1") {
		t.Error("tree not left untouched")
	}
	if run(t, a, "status", "--porcelain") != "" {
		t.Error("rebase left the tree dirty")
	}
}

func TestSyncOutRetriesOnceAfterConcurrentPush(t *testing.T) {
	a, b := checkouts(t)
	pin(t, b, pins("1", "2"), "b moves B")
	run(t, b, "push", "-q")
	pin(t, a, pins("2", "1"), "a moves A")

	var out bytes.Buffer
	(&Syncer{Out: &out, AutoPush: "1"}).SyncOut(a)
	if !strings.Contains(out.String(), "pushed (after rebasing onto a concurrent update)") {
		t.Fatalf("output: %s", out.String())
	}
	run(t, b, "pull", "-q")
	if read(t, b) != pins("2", "2") {
		t.Errorf("origin missing a's pin: %q", read(t, b))
	}
}

func TestSyncOutHonoursAutoPush(t *testing.T) {
	a, _ := checkouts(t)
	pin(t, a, pins("2", "1"), "a moves A")
	var out bytes.Buffer
	(&Syncer{Out: &out, AutoPush: "0"}).SyncOut(a)
	if !strings.Contains(out.String(), "not pushed (AUTO_PUSH=0)") {
		t.Errorf("output: %s", out.String())
	}
	if run(t, a, "rev-list", "--count", "@{upstream}..HEAD") != "1" {
		t.Error("pushed despite AUTO_PUSH=0")
	}
}

func TestWarnIfStaleAndCommit(t *testing.T) {
	a, b := checkouts(t)
	pin(t, b, pins("1", "2"), "b moves B")
	run(t, b, "push", "-q")

	var out bytes.Buffer
	s := &Syncer{Out: &out}
	s.WarnIfStale(a)
	if !strings.Contains(out.String(), "is 1 commit(s) behind origin") {
		t.Errorf("output: %s", out.String())
	}
	if read(t, a) != pins("1", "1") {
		t.Error("WarnIfStale changed the tree")
	}

	write(t, a, "versions.env", pins("5", "1"))
	if !s.Commit(a, "versions.env", "a: 1 -> 5") {
		t.Fatal("Commit failed")
	}
	if s.Commit(a, "versions.env", "nothing") {
		t.Error("Commit with nothing to commit reported success")
	}
	if s.Commit(t.TempDir(), "versions.env", "x") {
		t.Error("Commit outside a repo reported success")
	}
}
