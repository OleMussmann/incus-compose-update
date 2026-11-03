package apply

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OleMussmann/incus-compose-update/internal/incus"
	"github.com/OleMussmann/incus-compose-update/internal/pins"
	"github.com/OleMussmann/incus-compose-update/internal/registry"
)

const (
	oldDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// Two stacks. alpha manages db (digest mode, with a volume) and app (tag
// mode, depends on db); beta defines an unmanaged service also named db.
const alphaRaw = `services:
  db:
    image: docker.io/postgres:18-alpine@${DB_DIGEST}
  app:
    image: ghcr.io/acme/app:${APP_TAG}
`

const alphaRendered = `name: alpha
services:
  app:
    image: ghcr.io/acme/app:%s
    depends_on:
      db: {condition: service_healthy}
    x-update:
      tag_re: '^[0-9]+\.[0-9]+\.[0-9]+$'
  db:
    image: docker.io/postgres:18-alpine@%s
    x-update:
      mode: digest
      volume: vol-db
      version_cmd: [postgres, --version]
`

const betaRendered = `name: beta
services:
  db:
    image: docker.io/redis:7
`

type harness struct {
	t           *testing.T
	app         *App
	out         *bytes.Buffer
	alpha, beta string
	compose     *fakeCompose
	incus       *fakeIncus
	git         *fakeGit
	reg         *fakeRegistry
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{t: t, out: &bytes.Buffer{}}
	h.alpha, h.beta = filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	for dir, raw := range map[string]string{h.alpha: alphaRaw, h.beta: "services:\n  db:\n    image: docker.io/redis:7\n"} {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(raw), 0o644)
	}
	os.WriteFile(filepath.Join(h.alpha, "versions.env"), []byte("DB_DIGEST="+oldDigest+"\nAPP_TAG=1.0.0\n"), 0o644)

	h.incus = &fakeIncus{instances: map[string]*incus.Instance{}, volumes: []string{"vol-db"}}
	h.incus.set("db-1", true, "library/postgres@"+oldDigest)
	h.incus.set("app-1", true, "acme/app:1.0.0")
	h.compose = &fakeCompose{h: h, execOut: "postgres (PostgreSQL) 18.6"}
	h.git = &fakeGit{}
	h.reg = &fakeRegistry{
		digests: map[string]string{"library/postgres:18-alpine": newDigest},
		tags:    map[string][]string{"acme/app": {"1.0.0", "1.1.0", "latest", "0.9.9"}},
	}
	h.app = &App{
		Settings: Settings{Pool: "local", ReensureTimeout: "2m", ReadyTries: 3, ReadyInterval: time.Second},
		Dirs:     []string{h.alpha, h.beta},
		Compose:  h.compose, Incus: h.incus, Git: h.git, Registry: h.reg,
		Out: h.out, Err: h.out, In: strings.NewReader(""),
		Now:   func() time.Time { return time.Date(2026, 9, 22, 9, 49, 0, 0, time.UTC) },
		Sleep: func(time.Duration) {},
	}
	return h
}

func (h *harness) pin(name string) string {
	v, _ := pins.Read(filepath.Join(h.alpha, "versions.env"), name)
	return v
}

func (h *harness) wantOutput(parts ...string) {
	h.t.Helper()
	for _, p := range parts {
		if !strings.Contains(h.out.String(), p) {
			h.t.Errorf("output lacks %q:\n%s", p, h.out.String())
		}
	}
}

// --- fakes ---

type fakeCompose struct {
	h          *harness
	recreates  []string
	recreateFn func(service string) // replaces the default "recreate takes"
	recreateErr,
	reensureErr error
	execOut string
}

func (f *fakeCompose) Config(dir string) ([]byte, error) {
	if dir == f.h.beta {
		return []byte(betaRendered), nil
	}
	return []byte(fmt.Sprintf(alphaRendered, f.h.pin("APP_TAG"), f.h.pin("DB_DIGEST"))), nil
}

func (f *fakeCompose) Recreate(dir, service string) error {
	f.recreates = append(f.recreates, service)
	if f.recreateFn != nil {
		f.recreateFn(service)
	} else if service == "db" {
		f.h.incus.set("db-1", true, "library/postgres@"+f.h.pin("DB_DIGEST"))
	} else {
		f.h.incus.set("app-1", true, "acme/app:"+f.h.pin("APP_TAG"))
	}
	return f.recreateErr
}
func (f *fakeCompose) Start(string, string) error    { return nil }
func (f *fakeCompose) Reensure(string, string) error { return f.reensureErr }
func (f *fakeCompose) Exec(_, _ string, _ []string) string {
	return f.execOut
}
func (f *fakeCompose) Ps(string) error { return nil }

type fakeIncus struct {
	instances map[string]*incus.Instance
	volumes   []string
	snapErr   error
	snapshots []string
}

func (f *fakeIncus) set(name string, running bool, imageID string) {
	state := "STOPPED"
	if running {
		state = "RUNNING"
	}
	cfg := map[string]string{}
	if imageID != "" {
		cfg["image.id"] = imageID
	}
	f.instances[name] = &incus.Instance{Name: name, State: state, Running: running, Config: cfg}
}

func (f *fakeIncus) Instances(string) ([]incus.Instance, error) {
	var out []incus.Instance
	for _, i := range f.instances {
		out = append(out, *i)
	}
	return out, nil
}
func (f *fakeIncus) Instance(_, name string) (incus.Instance, error) {
	if i, ok := f.instances[name]; ok {
		return *i, nil
	}
	return incus.Instance{}, errors.New("not found")
}
func (f *fakeIncus) CustomVolumes(string, string) ([]string, error) { return f.volumes, nil }
func (f *fakeIncus) VolumeExists(_, _, v string) (bool, error) {
	for _, x := range f.volumes {
		if x == v {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeIncus) Snapshot(_, _, _, name string) error {
	if f.snapErr != nil {
		return f.snapErr
	}
	f.snapshots = append(f.snapshots, name)
	return nil
}

type fakeGit struct {
	syncErr error
	commits []string
	pushed  int
}

func (f *fakeGit) SyncIn(string) error { return f.syncErr }
func (f *fakeGit) WarnIfStale(string)  {}
func (f *fakeGit) Commit(_, _, msg string) bool {
	f.commits = append(f.commits, msg)
	return true
}
func (f *fakeGit) SyncOut(string) { f.pushed++ }

type fakeRegistry struct {
	digests map[string]string
	tags    map[string][]string
	// pushed holds newest-first tag lists for repos that report push order;
	// a repo absent from it behaves like a registry without push order.
	pushed map[string][]string
	err    error
}

func (f *fakeRegistry) Tags(_ context.Context, _, repo string) ([]string, error) {
	return f.tags[repo], f.err
}
func (f *fakeRegistry) PushedTags(_ context.Context, _, repo string, _ func(string) bool) ([]string, error) {
	if tags, ok := f.pushed[repo]; ok {
		return tags, nil
	}
	return nil, registry.ErrNoPushOrder
}
func (f *fakeRegistry) Digest(_ context.Context, _, repo, tag string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.digests[repo+":"+tag], nil
}

// --- apply ---

func TestApplyHappyPath(t *testing.T) {
	h := newHarness(t)
	h.compose.execOut = "postgres (PostgreSQL) 18.6"
	if err := h.app.Apply(context.Background(), "db", newDigest, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if h.pin("DB_DIGEST") != newDigest {
		t.Error("pin not written")
	}
	if len(h.incus.snapshots) != 1 || h.incus.snapshots[0] != "pre-sha256-222222222222-202609220949" {
		t.Errorf("snapshots = %v", h.incus.snapshots)
	}
	if len(h.git.commits) != 1 || h.git.commits[0] != "db: "+oldDigest+" -> "+newDigest || h.git.pushed != 1 {
		t.Errorf("commits %v, pushed %d", h.git.commits, h.git.pushed)
	}
	h.wantOutput("==> verified library/postgres@"+newDigest, "(unchanged — rebuild of the same release)",
		"==> done. rollback: incus-compose-update apply db "+oldDigest)
}

func TestApplyPicksTheManagingStack(t *testing.T) {
	// beta defines an unmanaged db and comes second; alpha's must be chosen
	// whatever the order.
	h := newHarness(t)
	h.app.Dirs = []string{h.beta, h.alpha}
	if err := h.app.Apply(context.Background(), "db", newDigest, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
}

func TestApplyRefusesAmbiguousName(t *testing.T) {
	h := newHarness(t)
	h.app.Dirs = []string{h.alpha, h.alpha}
	err := h.app.Apply(context.Background(), "db", newDigest, "")
	if err == nil || !strings.Contains(err.Error(), "managed in several stacks") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyRefusesUnmanaged(t *testing.T) {
	h := newHarness(t)
	h.app.Dirs = []string{h.beta}
	err := h.app.Apply(context.Background(), "db", newDigest, "")
	if err == nil || !strings.Contains(err.Error(), "has no x-update: block") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplySnapshotFailureChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.incus.snapErr = errors.New("Invalid storage volume snapshot name")
	err := h.app.Apply(context.Background(), "db", newDigest, "")
	if err == nil {
		t.Fatal("apply succeeded despite a failed snapshot")
	}
	if h.pin("DB_DIGEST") != oldDigest || len(h.compose.recreates) != 0 {
		t.Errorf("pin %s, recreates %v", h.pin("DB_DIGEST"), h.compose.recreates)
	}
	h.wantOutput("could not snapshot vol-db — nothing was changed")
}

func TestApplyStaleCheckoutStopsBeforeSnapshot(t *testing.T) {
	h := newHarness(t)
	other := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	h.incus.instances["db-1"].Config = map[string]string{"user.image_alias": "docker.io/library/postgres@" + other}
	err := h.app.Apply(context.Background(), "db", newDigest, "")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v", err)
	}
	if len(h.incus.snapshots) != 0 || h.pin("DB_DIGEST") != oldDigest {
		t.Error("something was touched")
	}
	// The adopt suggestion keeps the digest's algorithm prefix.
	h.wantOutput("does not match the server", "incus-compose-update apply db "+other)
}

func TestApplyTowardsWhatAlreadyRuns(t *testing.T) {
	h := newHarness(t)
	h.incus.set("db-1", true, "library/postgres@"+newDigest)
	if err := h.app.Apply(context.Background(), "db", newDigest, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	h.wantOutput("already runs " + newDigest)
}

func TestApplyRecreateFailureIsLeftToVerification(t *testing.T) {
	h := newHarness(t)
	h.compose.recreateErr = errors.New("exit 1")
	if err := h.app.Apply(context.Background(), "db", newDigest, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	h.wantOutput("recreate exited non-zero — continuing to verify", "==> verified")
}

func TestApplyDetectsVanishedAndStoppedInstances(t *testing.T) {
	for name, fn := range map[string]func(*harness){
		"vanished": func(h *harness) { delete(h.incus.instances, "app-1") },
		"stopped":  func(h *harness) { h.incus.set("app-1", false, "acme/app:1.0.0") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.incus.set("minio-1", false, "") // already stopped: not this apply's doing
			h.compose.recreateFn = func(string) {
				h.incus.set("db-1", true, "library/postgres@"+newDigest)
				fn(h)
			}
			err := h.app.Apply(context.Background(), "db", newDigest, "")
			if !errors.Is(err, ErrReported) {
				t.Fatalf("err = %v", err)
			}
			if name == "vanished" {
				h.wantOutput("gone after it:", "     app-1")
			} else {
				h.wantOutput("down after it:", "     app-1 (STOPPED)")
			}
			if strings.Contains(h.out.String(), "minio-1") {
				t.Error("pre-stopped instance reported")
			}
			if len(h.git.commits) != 0 {
				t.Error("committed despite an incomplete stack")
			}
		})
	}
}

func TestApplyVerificationFailures(t *testing.T) {
	cases := map[string]struct {
		setup func(*harness)
		want  string
	}{
		"old image": {
			func(h *harness) { h.compose.recreateFn = func(string) {} },
			"is not running the new image",
		},
		"no identity": {
			func(h *harness) {
				h.compose.recreateFn = func(string) { h.incus.set("db-1", true, "") }
			},
			"cannot read image identity",
		},
		"silent probe": {
			func(h *harness) { h.compose.execOut = "" },
			"did not respond in 3s",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			c.setup(h)
			err := h.app.Apply(context.Background(), "db", newDigest, "")
			if !errors.Is(err, ErrReported) {
				t.Fatalf("err = %v", err)
			}
			h.wantOutput(c.want, "roll back:  incus-compose-update apply db "+oldDigest,
				"or restore: incus --project alpha storage volume restore local vol-db pre-sha256-222222222222-202609220949")
			if len(h.git.commits) != 0 {
				t.Error("committed an unverified apply")
			}
		})
	}
}

func TestApplyMissingVolume(t *testing.T) {
	h := newHarness(t)
	h.incus.volumes = []string{"vol-other"}
	err := h.app.Apply(context.Background(), "db", newDigest, "")
	if err == nil || !strings.Contains(err.Error(), "set INCUS_POOL or x-update.volume") {
		t.Fatalf("err = %v", err)
	}
	h.wantOutput("cannot find volume 'vol-db'", "vol-other")
}

func TestSnapshotName(t *testing.T) {
	now := time.Date(2026, 9, 22, 9, 49, 0, 0, time.UTC)
	for ref, want := range map[string]string{
		newDigest:               "pre-sha256-222222222222-202609220949",
		"2.11.212":              "pre-2.11.212-202609220949",
		"v2026.8.18":            "pre-v2026.8.18-202609220949",
		"a/b:c":                 "pre-a-b-c-202609220949",
		strings.Repeat("x", 80): "pre-" + strings.Repeat("x", 47) + "-202609220949",
	} {
		got := snapshotName(ref, now)
		if got != want || len(got) > 64 {
			t.Errorf("snapshotName(%q) = %q (%d)", ref, got, len(got))
		}
	}
}

// --- apply-all ---

func TestApplyAllOrdersDependenciesFirst(t *testing.T) {
	h := newHarness(t)
	if err := h.app.ApplyAll(context.Background(), true); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	// app renders first but depends on db.
	if strings.Join(h.compose.recreates, ",") != "db,app" {
		t.Errorf("recreates = %v", h.compose.recreates)
	}
	if h.pin("APP_TAG") != "1.1.0" || h.pin("DB_DIGEST") != newDigest {
		t.Errorf("pins %s %s", h.pin("APP_TAG"), h.pin("DB_DIGEST"))
	}
	h.wantOutput("planned updates:", "db                     "+oldDigest+" -> "+newDigest, "applied 2 update(s)")
}

func TestApplyAllResolvesEverythingBeforeMutating(t *testing.T) {
	h := newHarness(t)
	h.reg.tags["acme/app"] = []string{"latest"} // tag_re matches nothing
	err := h.app.ApplyAll(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "update tag_re for app") {
		t.Fatalf("err = %v", err)
	}
	if len(h.compose.recreates) != 0 || len(h.incus.snapshots) != 0 {
		t.Error("mutated before the plan was complete")
	}
}

func TestApplyAllNeedsConsent(t *testing.T) {
	h := newHarness(t)
	err := h.app.ApplyAll(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("non-interactive: %v", err)
	}

	h = newHarness(t)
	h.app.Interactive, h.app.In = true, strings.NewReader("n\n")
	if err := h.app.ApplyAll(context.Background(), false); err != nil || len(h.compose.recreates) != 0 {
		t.Fatalf("declined: %v, recreates %v", err, h.compose.recreates)
	}
	h.wantOutput("recreate 2 service(s), one at a time? [y/N] ", "aborted — nothing was changed")

	h = newHarness(t)
	h.app.Interactive, h.app.In = true, strings.NewReader("y\n")
	if err := h.app.ApplyAll(context.Background(), false); err != nil || len(h.compose.recreates) != 2 {
		t.Fatalf("accepted: %v, recreates %v", err, h.compose.recreates)
	}
}

func TestApplyAllStopsAtFirstFailure(t *testing.T) {
	h := newHarness(t)
	h.incus.snapErr = errors.New("rejected")
	err := h.app.ApplyAll(context.Background(), true)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v", err)
	}
	if len(h.compose.recreates) != 0 || h.pin("APP_TAG") != "1.0.0" {
		t.Error("continued past the failure")
	}
	h.wantOutput("error: rejected", "!! stopped at db — 0 of 2 applied", "the remaining 1 service(s) are untouched")
}

func TestApplyAllNothingToDo(t *testing.T) {
	h := newHarness(t)
	h.reg.digests["library/postgres:18-alpine"] = oldDigest
	h.reg.tags["acme/app"] = []string{"1.0.0"}
	if err := h.app.ApplyAll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	h.wantOutput("everything is up to date")
}

// --- check ---

func TestCheckReportsAndContinuesPastFailures(t *testing.T) {
	h := newHarness(t)
	h.reg.digests = map[string]string{} // Digest returns "" ...
	h.reg.err = nil
	h.app.Registry = &failingDigest{h.reg}
	err := h.app.Check(context.Background(), "")
	if err == nil || err.Error() != "1 service(s) could not be checked" {
		t.Fatalf("err = %v\n%s", err, h.out)
	}
	// db failed, app was still checked.
	h.wantOutput("!! db: could not fetch digest", "== app", "apply:     incus-compose-update apply app 1.1.0")
}

type failingDigest struct{ *fakeRegistry }

func (failingDigest) Digest(context.Context, string, string, string) (string, error) {
	return "", errors.New("docker.io/library/postgres:18-alpine: not found")
}

func TestCheckOneService(t *testing.T) {
	h := newHarness(t)
	if err := h.app.Check(context.Background(), "db"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	h.wantOutput("== db\ncurrent: " + oldDigest + "\nlatest:  " + newDigest + "\napply:     incus-compose-update apply db " + newDigest)
	if strings.Contains(h.out.String(), "== app") {
		t.Error("checked more than the named service")
	}
}

func TestCheckWarnsOnTagSchemeChange(t *testing.T) {
	h := newHarness(t)
	h.reg.tags["acme/app"] = []string{"1.0.0", "v0.22.6", "v0.22.5"}
	h.reg.pushed = map[string][]string{"acme/app": {"latest", "v0.22.6", "v0.22.6-desktop", "v0.22.5", "1.0.0"}}
	if err := h.app.Check(context.Background(), "app"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	h.wantOutput("up to date",
		"!! app: 2 release-shaped tag(s) pushed since the newest tag_re match are rejected by tag_re: v0.22.6 v0.22.5",
		"upstream may have changed its tag scheme")
}

func TestCheckNoDriftWarningWithoutPushOrder(t *testing.T) {
	h := newHarness(t)
	if err := h.app.Check(context.Background(), ""); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if strings.Contains(h.out.String(), "tag scheme") {
		t.Errorf("unexpected drift warning:\n%s", h.out)
	}
}

// --- dry run ---

func TestDryRunChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.app.DryRun = true
	h.git.syncErr = errors.New("SyncIn must not run in a dry run")
	if err := h.app.Apply(context.Background(), "db", newDigest, ""); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if err := h.app.ApplyAll(context.Background(), false); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if h.pin("DB_DIGEST") != oldDigest || len(h.incus.snapshots) != 0 || len(h.compose.recreates) != 0 || len(h.git.commits) != 0 {
		t.Error("dry run changed something")
	}
	h.wantOutput("--> would snapshot alpha:local/vol-db as pre-sha256-222222222222-202609220949",
		"--> would pin DB_DIGEST: "+oldDigest+" -> "+newDigest, "planned updates:", "dry run — nothing was changed")
}

func TestDryRunStillRunsReadOnlyChecks(t *testing.T) {
	h := newHarness(t)
	h.app.DryRun = true
	h.incus.volumes = nil
	if err := h.app.Apply(context.Background(), "db", newDigest, ""); err == nil {
		t.Fatal("dry run skipped the volume preflight")
	}
}
