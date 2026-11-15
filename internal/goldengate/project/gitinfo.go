package project

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// maxGitFile bounds what GitInfo reads from a repository's metadata.
const maxGitFile = 64 << 10

// GitInfo reads a folder's repository remote and current branch from its git
// metadata (the HEAD and config files): two small files, read-only, never
// any tracked content. It returns empty strings for a folder that is not a
// repository or whose metadata does not look as expected. Callers use it
// only when the user turned on enrich.git.
//
// The remote is the URL of "origin", or of the first remote; it may carry
// credentials, so normalize it (rules.NormalizeRemote) before showing it.
func GitInfo(dir string) (remote, branch string) {
	gitdir := filepath.Join(dir, ".git")
	st, err := os.Lstat(gitdir)
	if err != nil {
		return "", ""
	}
	if st.Mode().IsRegular() { // a linked worktree: ".git" points at its own git directory
		line := firstLine(gitdir)
		p, ok := strings.CutPrefix(line, "gitdir: ")
		if !ok || p == "" {
			return "", ""
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		gitdir = filepath.Clean(p)
	} else if !st.IsDir() {
		return "", ""
	}
	if ref, ok := strings.CutPrefix(firstLine(filepath.Join(gitdir, "HEAD")), "ref: refs/heads/"); ok {
		branch = ref
	}
	// a worktree's config lives in the main git directory
	common := gitdir
	if c := firstLine(filepath.Join(gitdir, "commondir")); c != "" {
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		common = filepath.Clean(c)
	}
	return remoteURL(filepath.Join(common, "config")), branch
}

// smallFile opens a regular file no larger than maxGitFile.
func smallFile(p string) *os.File {
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxGitFile {
		return nil
	}
	f, err := os.Open(p) //nolint:gosec // G304: a git metadata file of a folder the tools already recorded
	if err != nil {
		return nil
	}
	return f
}

func firstLine(p string) string {
	f := smallFile(p)
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	line, _ := bufio.NewReader(f).ReadString('\n')
	return strings.TrimSpace(line)
}

// remoteURL reads the url of [remote "origin"], else of the first remote,
// from a git config file.
func remoteURL(p string) string {
	f := smallFile(p)
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var first, origin, section string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = ""
			if rest, ok := strings.CutPrefix(line, `[remote "`); ok {
				section, _, _ = strings.Cut(rest, `"`)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section == "" || strings.TrimSpace(k) != "url" {
			continue
		}
		v = strings.TrimSpace(v)
		if first == "" {
			first = v
		}
		if section == "origin" && origin == "" {
			origin = v
		}
	}
	if origin != "" {
		return origin
	}
	return first
}
