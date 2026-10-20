// Package applypatch lists the files an "apply_patch" envelope touches. Codex
// and OpenCode share the format. File contents inside the patch are never
// returned.
package applypatch

import "strings"

// Op is what a patch does to a file.
type Op int

// Operations.
const (
	Add Op = iota
	Update
	Delete
)

// File is one file touched by a patch.
type File struct {
	Op   Op
	Path string
}

// Parse returns the files named by "*** Add/Update/Delete File:" headers.
func Parse(patch string) []File {
	var out []File
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimRight(line, "\r")
		for prefix, op := range map[string]Op{"*** Add File: ": Add, "*** Update File: ": Update, "*** Delete File: ": Delete} {
			if strings.HasPrefix(line, prefix) {
				if p := strings.TrimSpace(line[len(prefix):]); p != "" {
					out = append(out, File{op, p})
				}
			}
		}
	}
	return out
}
