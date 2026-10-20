// Package pathutil normalizes filesystem paths for project identity, selection
// matching and scrubbing.
package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CaseInsensitiveFS reports whether paths on this platform are compared
// case-insensitively by default (Windows and macOS).
func CaseInsensitiveFS() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

// ExpandHome expands a leading "~" using home.
func ExpandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Normalize returns an absolute, cleaned path with symlinks resolved when the
// path (or its longest existing ancestor) exists. It never touches the
// filesystem beyond stat/readlink.
func Normalize(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	return resolveExisting(abs)
}

// resolveExisting resolves symlinks in the longest existing prefix of abs and
// re-appends the rest, so paths of deleted folders still normalize stably.
func resolveExisting(abs string) string {
	rest := ""
	cur := abs
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// Fold returns the comparison form of a path: slash-separated and, when
// caseInsensitive, lower-cased.
func Fold(p string, caseInsensitive bool) string {
	p = filepath.ToSlash(p)
	if caseInsensitive {
		p = strings.ToLower(p)
	}
	return p
}

// Within reports whether child is root or lies beneath it (slash form, already folded).
func Within(child, root string) bool {
	if child == root {
		return true
	}
	root = strings.TrimSuffix(root, "/")
	return strings.HasPrefix(child, root+"/")
}

// Home returns the user's home directory, or "".
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// Scrub replaces the home directory prefix with "~".
func Scrub(p, home string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	h := strings.TrimSuffix(home, string(filepath.Separator))
	if strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + filepath.ToSlash(p[len(h):])
	}
	return p
}
