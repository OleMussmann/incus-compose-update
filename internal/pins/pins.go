// Package pins reads and writes version pins in a versions.env file.
//
// The file is plain KEY=value lines with comments. This tool is its only
// writer; everything else in the file is preserved byte for byte.
package pins

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Read returns the value of the first `name=` line, or "" if there is none
// or the file does not exist.
func Read(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	prefix := name + "="
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), prefix); ok {
			return v, nil
		}
	}
	return "", sc.Err()
}

// Write sets name to value: every existing `name=` line is rewritten, or a
// new line is appended. The file is replaced atomically, keeping its mode.
func Write(path, name, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}

	prefix := name + "="
	line := prefix + value
	lines := strings.SplitAfter(string(data), "\n")
	found := false
	for i, l := range lines {
		body := strings.TrimSuffix(l, "\n")
		if strings.HasPrefix(body, prefix) {
			lines[i] = line + l[len(body):]
			found = true
		}
	}
	out := strings.Join(lines, "")
	if !found {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += line + "\n"
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".versions.env.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
