package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpAnywhereNeedsNoConfig(t *testing.T) {
	t.Setenv("STACK_DIRS", "")
	t.Setenv("STACK_DIR", "")
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"apply-all", "--help"},
		{"apply", "firecrawl", "-h"},
		{"check", "--dry-run", "--help"},
	} {
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Errorf("%v: exit %d", args, code)
		}
		if !strings.HasPrefix(out.String(), "usage:") {
			t.Errorf("%v: no usage text:\n%s", args, out.String())
		}
	}
}

func TestUsageShowsDryRun(t *testing.T) {
	for _, line := range []string{
		"apply [--dry-run] <service> <ref>",
		"apply-all [--dry-run] [--yes]",
	} {
		if !strings.Contains(usage, line) {
			t.Errorf("usage lacks %q", line)
		}
	}
}
