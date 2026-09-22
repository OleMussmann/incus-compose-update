// Package compose runs incus-compose for a stack directory.
//
// Everything goes through the incus-compose CLI on purpose: its render is the
// deploy's source of truth, and its up/start graph semantics must not be
// re-derived here.
package compose

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
)

// Compose is what the updater needs from incus-compose.
type Compose interface {
	// Config returns the rendered definition (`incus-compose config`).
	Config(dir string) ([]byte, error)
	// Recreate pulls and recreates one service.
	Recreate(dir, service string) error
	// Start starts one service. Callers use it as a no-op when running.
	Start(dir, service string) error
	// Reensure brings the whole stack up without recreating or pulling.
	Reensure(dir, dependencyTimeout string) error
	// Exec runs argv in the service's instance and returns its stdout,
	// whatever the exit status: some version commands print and exit
	// non-zero. Empty output means no answer.
	Exec(dir, service string, argv []string) string
	// Ps prints the stack's instances.
	Ps(dir string) error
}

// CLI is the Compose backed by the incus-compose binary on PATH.
type CLI struct {
	Stdout, Stderr io.Writer
}

// cmd builds an incus-compose invocation in dir. --env-file replaces the
// default .env, so both files are always passed, pins last: variables then
// resolve exactly as they do at deploy time.
func cmd(dir string, args ...string) *exec.Cmd {
	c := exec.Command("incus-compose", append([]string{"--env-file", ".env", "--env-file", "versions.env"}, args...)...)
	c.Dir = dir
	return c
}

// Config implements Compose. stderr carries "variable is not set" warnings
// and is discarded.
func (c *CLI) Config(dir string) ([]byte, error) {
	return cmd(dir, "config").Output()
}

func (c *CLI) stream(x *exec.Cmd) error {
	x.Stdout, x.Stderr = c.Stdout, c.Stderr
	return x.Run()
}

// Recreate implements Compose.
func (c *CLI) Recreate(dir, service string) error {
	return c.stream(cmd(dir, "up", "-d", "--recreate", "--pull", "always", service))
}

// Start implements Compose. Output is discarded: on a running service it
// only reports "resource is already running".
func (c *CLI) Start(dir, service string) error {
	return cmd(dir, "start", service).Run()
}

// Reensure implements Compose.
func (c *CLI) Reensure(dir, timeout string) error {
	return c.stream(cmd(dir, "up", "-d", "--dependency-timeout", timeout))
}

// Exec implements Compose. exec parses the compose file to find the
// instance, which is why it needs the env files too.
func (c *CLI) Exec(dir, service string, argv []string) string {
	var out bytes.Buffer
	x := cmd(dir, append([]string{"exec", service, "--"}, argv...)...)
	x.Stdout = &out
	_ = x.Run()
	return strings.TrimRight(out.String(), "\n")
}

// Ps implements Compose.
func (c *CLI) Ps(dir string) error {
	return c.stream(cmd(dir, "ps"))
}
