// Command incus-compose-update is a generic incus-compose image updater. It
// reads per-service update metadata from compose.yaml (x-update: blocks) and
// manages pins in a tracked versions.env file.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OleMussmann/incus-compose-update/internal/apply"
	"github.com/OleMussmann/incus-compose-update/internal/compose"
	"github.com/OleMussmann/incus-compose-update/internal/gitsync"
	"github.com/OleMussmann/incus-compose-update/internal/incus"
	"github.com/OleMussmann/incus-compose-update/internal/registry"
	"github.com/OleMussmann/incus-compose-update/internal/stack"
	"golang.org/x/term"
)

const usage = `usage:
  incus-compose-update check                  compare pinned tags against upstream, all services
  incus-compose-update check <service>        same, scoped to one service
  incus-compose-update apply <service> <ref>  snapshot, pin, recreate, verify
  incus-compose-update apply-all [--yes]      apply every pending update, one at a time
  incus-compose-update --help                 this text

  apply and apply-all take --dry-run: run every read-only check and print
  what would change, touching neither the checkout nor the server.

config (environment):
  STACK_DIRS    colon-separated stack directories (required, or STACK_DIR)
  STACK_DIR     single-stack directory (alternative to STACK_DIRS)
  INCUS_POOL    storage pool (default: local)
  INCUS_PROJECT incus project (default: detected from rendered config)
  AUTO_PUSH     push each pin commit (default: 1; set 0 to commit only)
  REENSURE_TIMEOUT  dependency timeout for the post-recreate stack-wide up
                    (default: 2m)

git:
  A run pulls each stack dir before reading its pins, refuses to apply when a
  pin does not match the running instance, and pushes the commit it writes.
  Git problems are reported, never fatal to a verified deploy.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// --help must work without STACK_DIRS set.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Print(usage)
		return 0
	}
	if len(args) == 0 {
		args = []string{"check"}
	}

	app, err := newApp()
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()

	cmd, rest := args[0], args[1:]
	flags := map[string]bool{}
	var pos []string
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			flags[a] = true
		} else {
			pos = append(pos, a)
		}
	}
	allowed := map[string][]string{"check": nil, "apply": {"--dry-run"}, "apply-all": {"--dry-run", "--yes", "-y"}}
	for f := range flags {
		if ok, known := allowed[cmd]; known && !slices.Contains(ok, f) {
			return fail(fmt.Errorf("unknown option for %s: %s", cmd, f))
		}
	}
	app.DryRun = flags["--dry-run"]

	switch cmd {
	case "check":
		err = app.Check(ctx, arg(pos, 0))
	case "apply":
		err = app.Apply(ctx, arg(pos, 0), arg(pos, 1), "")
	case "apply-all":
		if len(pos) > 0 {
			return fail(fmt.Errorf("unknown argument for apply-all: %s", pos[0]))
		}
		err = app.ApplyAll(ctx, flags["--yes"] || flags["-y"])
	default:
		return fail(errors.New("usage: incus-compose-update {check [service] | apply <service> <ref> | apply-all [--yes]}"))
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func fail(err error) int {
	if !errors.Is(err, apply.ErrReported) {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return 1
}

func newApp() (*apply.App, error) {
	dirs, err := stack.Dirs()
	if err != nil {
		return nil, err
	}
	tries, err := envInt("READY_TRIES", 20)
	if err != nil {
		return nil, err
	}
	interval, err := envInt("READY_INTERVAL", 3)
	if err != nil {
		return nil, err
	}

	return &apply.App{
		Settings: apply.Settings{
			Pool:            envOr("INCUS_POOL", "local"),
			Project:         os.Getenv("INCUS_PROJECT"),
			ReensureTimeout: envOr("REENSURE_TIMEOUT", "2m"),
			ReadyTries:      tries,
			ReadyInterval:   time.Duration(interval) * time.Second,
		},
		Dirs:     dirs,
		Compose:  &compose.CLI{Stdout: os.Stdout, Stderr: os.Stderr},
		Incus:    &incus.Server{},
		Git:      &gitsync.Syncer{Out: os.Stdout, AutoPush: envOr("AUTO_PUSH", "1")},
		Registry: &registry.Client{},
		Out:      os.Stdout,
		Err:      os.Stderr,
		In:       os.Stdin,
		// A real terminal, not just a character device: /dev/null is one too.
		Interactive: term.IsTerminal(int(os.Stdin.Fd())),
		Now:         time.Now,
		Sleep:       time.Sleep,
	}, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", key, v)
	}
	return n, nil
}
