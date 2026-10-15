// Package sysx is the thin OS layer: process spawning, file probes and
// atomic writes. Nothing here knows about twin's domain.
package sysx

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result is a finished child process.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Run executes a program with the current environment.
func Run(name string, args ...string) (Result, error) {
	return RunEnv(nil, name, args...)
}

// RunEnv executes a program. When env is non-nil it replaces the child's
// environment entirely (that is what we want for WAYLAND_DEBUG probes).
func RunEnv(env []string, name string, args ...string) (Result, error) {
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = env
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Code = ee.ExitCode()
			return res, nil // a non-zero exit is data, not a failure to run
		}
		return res, fmt.Errorf("run %s: %w", name, err)
	}
	return res, nil
}

// RunInteractive executes a program attached to this process's own terminal.
//
// It exists for exactly one caller: the sudo that installs the AppArmor
// profile. Run captures a child's output, which would also capture sudo's
// password prompt — and a prompt nobody can read is a hang, not a question.
func RunInteractive(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(append([]string{name}, args...), " "), err)
	}
	return nil
}

// Which finds an executable on PATH — or at an absolute path.
//
// Note: plain lookPath is fine here because PATH inside a normal desktop
// session is the one we want to reason about.
func Which(name string) string {
	if strings.ContainsRune(name, '/') {
		if IsExecutableFile(name) {
			return name
		}
		return ""
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// FirstInstalled returns the first candidate that resolves, or "".
func FirstInstalled(candidates ...string) string {
	for _, c := range candidates {
		if p := Which(c); p != "" {
			return p
		}
	}
	return ""
}

// Exists reports whether path exists (any type), following symlinks.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsRegularFile reports whether path exists and is a regular file (following
// symlinks, like the shell's `-f`). It is the probe behind the directory/file
// distinction in path mappings: a mapping whose two ends are files needs its
// mount point *created as a file*, which mkdir -p cannot express.
func IsRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// IsExecutableFile reports whether path is a regular file with any execute bit.
//
// This is the exact condition GLib checks before accepting a desktop entry's
// Exec target; when it fails, GLib returns NULL and the whole entry vanishes
// silently (see 应用包裹生成器_twin设计.md §5.3 rule 4).
func IsExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return st.Mode().Perm()&0o111 != 0
}

// EnsureDir creates a directory (and parents) if missing.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

// EnsureFile creates an empty file (and its parents) if missing, and leaves an
// existing file alone — a mapping's file end is the program's own data, and
// truncating it to "make sure it exists" would throw that data away.
//
// It is EnsureDir's counterpart: a mount point that has to exist as a *file* is
// the one thing MkdirAll cannot produce.
func EnsureFile(path string) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// WriteFileAtomic writes data to a temp file in the same directory and renames
// it into place, so a half-written artefact can never be observed.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // no-op once renamed
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadFile is a convenience wrapper returning a string.
func ReadFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// pythonCandidates is ordered: the distribution interpreter comes first because
// a toolchain-managed python3 (a pyenv/rye/uv shim, or the one WorkBuddy ships)
// is frequently on PATH and frequently cannot import gi.
var pythonCandidates = []string{
	"/usr/bin/python3",
	"/usr/local/bin/python3",
	"python3",
}

// PythonWithGI returns a python3 that can actually `import gi`, or "".
//
// This distinction matters: reporting "no python3-gi installed" when the real
// problem is "the python3 on PATH is not the distribution one" sends the user
// off to install a package they already have.
func PythonWithGI() string {
	for _, cand := range pythonCandidates {
		p := Which(cand)
		if p == "" {
			continue
		}
		res, err := Run(p, "-c", "import gi")
		if err == nil && res.Code == 0 {
			return p
		}
	}
	return ""
}

// Python3 returns any usable python3 interpreter.
func Python3() string {
	for _, cand := range pythonCandidates {
		if p := Which(cand); p != "" {
			return p
		}
	}
	return ""
}
