package pins

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `# Version pins. Comments survive.
FIRECRAWL_TAG=2.11.384

# SearXNG
SEARXNG_TAG=2026.9.22-019460e07
`

func TestReadWriteRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "versions.env")
	if err := os.WriteFile(p, []byte(sample), 0o640); err != nil {
		t.Fatal(err)
	}

	if v, _ := Read(p, "SEARXNG_TAG"); v != "2026.9.22-019460e07" {
		t.Errorf("Read = %q", v)
	}
	if v, _ := Read(p, "FIRECRAWL"); v != "" {
		t.Errorf("prefix of another name matched: %q", v)
	}

	if err := Write(p, "FIRECRAWL_TAG", "2.11.400"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := `# Version pins. Comments survive.
FIRECRAWL_TAG=2.11.400

# SearXNG
SEARXNG_TAG=2026.9.22-019460e07
`
	if string(got) != want {
		t.Errorf("after Write:\n%s", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", st.Mode().Perm())
	}
}

func TestWriteAppendsAfterMissingNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "versions.env")
	os.WriteFile(p, []byte("A=1"), 0o644)
	if err := Write(p, "B", "2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "A=1\nB=2\n" {
		t.Errorf("got %q", got)
	}
}

func TestMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "versions.env")
	if v, err := Read(p, "X"); v != "" || err != nil {
		t.Errorf("Read missing = %q, %v", v, err)
	}
	if err := Write(p, "X", "1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "X=1\n" {
		t.Errorf("got %q", got)
	}
}
