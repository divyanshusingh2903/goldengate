// Package project resolves which repository a session's folder belongs to, so
// that worktrees of one repository group together (and are stored under one
// project on the server) while the folder itself stays the unit of selection.
//
// Resolution is path-based by default: it never reads file contents. Reading
// the small ".git" pointer file of linked worktrees is an opt-in (enrich.git).
package project

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Resolver finds repository roots.
type Resolver struct {
	// ReadGitPointers lets the resolver read the first line of a ".git" *file*
	// (what linked worktrees have) to find the main repository. Off by default.
	ReadGitPointers bool
}

// claudeWorktrees is where Claude Code puts its worktrees, inside the repo.
const claudeWorktrees = "/.claude/worktrees/"

// Root returns the repository root a session belongs to when it differs from
// its own folder, or "" when the folder is its own project. Sources:
// the tool's own record (OpenCode's project worktree), the Claude Code
// worktree layout, and (opt-in) the .git pointer file.
func (r Resolver) Root(ref model.SessionRef) string {
	key := ref.ProjectKey
	if key == "" || pathutil.IsVirtualKey(key) {
		return ""
	}
	if ref.RepoRoot != "" && ref.RepoRoot != key && isAncestorOrSibling(ref.RepoRoot, key) {
		return ref.RepoRoot
	}
	if root := r.fromPath(key); root != "" {
		return root
	}
	if r.ReadGitPointers {
		return readGitPointer(key)
	}
	return ""
}

// isAncestorOrSibling guards the tool-supplied root: it must be a plausible
// absolute path, not something that merges unrelated folders.
func isAncestorOrSibling(root, key string) bool {
	return filepath.IsAbs(root) && root != string(filepath.Separator) && filepath.IsAbs(key) && root != filepath.Dir(root)
}

// RootOfPath is Root for callers that only have a folder path.
func (r Resolver) RootOfPath(path string) string { return r.Root(model.SessionRef{ProjectKey: path}) }

func (Resolver) fromPath(key string) string {
	slashed := filepath.ToSlash(key)
	if i := strings.Index(slashed, claudeWorktrees); i > 0 {
		rest := slashed[i+len(claudeWorktrees):]
		if rest != "" && rest != "/" { // there must be a worktree name after the marker
			return filepath.FromSlash(slashed[:i])
		}
	}
	return ""
}

// readGitPointer reads "gitdir: <main>/.git/worktrees/<name>" from a linked
// worktree's .git file. Directories (normal repositories) are not read.
func readGitPointer(dir string) string {
	p := filepath.Join(dir, ".git")
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4096 {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReader(f).ReadString('\n')
	if line == "" && err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir: ")
	if !ok {
		return ""
	}
	slashed := filepath.ToSlash(gitdir)
	if !filepath.IsAbs(gitdir) {
		slashed = filepath.ToSlash(filepath.Join(dir, gitdir))
	}
	const marker = "/.git/worktrees/"
	if i := strings.Index(slashed, marker); i > 0 {
		return pathutil.Normalize(filepath.FromSlash(slashed[:i]))
	}
	return ""
}
