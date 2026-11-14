package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// Project is a space the user names (DESIGN.md 1.1, principle 7). Its members
// are what it is made of: folders or repo roots, folder globs, and chat
// groupings such as "claude-ai:Chats". Being a member of a project means being
// shared, exactly as an allow rule did before projects existed.
//
// A folder may belong to one project only. When members nest (a folder in one
// project, a folder inside it in another), the most specific member decides
// which project a session belongs to.
type Project struct {
	// ID is stable for the life of the project; renaming keeps it.
	ID   string `toml:"id"`
	Name string `toml:"name"`
	// Members are folders, globs or chat keys, written as the user wrote them
	// (usually ~/... for folders).
	Members []Member `toml:"members,omitempty"`
	// WorkItems are work-item key prefixes that belong to this project, such
	// as "linear:ENG". Rules (a later issue) use them; matching ignores them.
	WorkItems []string `toml:"work_items,omitempty"`
}

// Member is one thing a project is made of.
type Member struct {
	Path string `toml:"path"`
	// ExcludeSources lists tools whose sessions are not sent for this member
	// while the rest of it is ("codex"). It replaces share.deny_source.
	ExcludeSources []string `toml:"exclude_sources,omitempty"`
}

// The share rules other code edits (Share.Allow and Share.SourceDeny) are a
// view of the projects: flatten derives them, fold writes edits made through
// them back into the projects. Load flattens; Save folds. Code that edits
// Projects directly calls Flatten afterwards (the Project* helpers do).

// Flatten rebuilds Share.Allow and Share.SourceDeny from the projects. Source
// rules that name no member are kept as they are.
func (c *Config) Flatten() {
	var allow, sd []string
	for _, p := range c.Projects {
		for _, m := range p.Members {
			allow = append(allow, m.Path)
			for _, s := range m.ExcludeSources {
				sd = append(sd, selection.SourceRule(s, m.Path))
			}
		}
	}
	for _, r := range c.Share.SourceDeny {
		if _, pat, ok := selection.SplitSourceRule(r); ok && c.memberIndex(pat) == nil && !slices.Contains(sd, r) {
			sd = append(sd, r)
		}
	}
	c.Share.Allow, c.Share.SourceDeny = allow, sd
}

// migrate brings a configuration as read from disk into its in-memory form:
// the views are derived from the projects, and allow and deny_source rules
// written before projects existed are folded in (each allow rule becomes a
// project of its own).
func (c *Config) migrate() {
	legacyAllow, legacySD := c.Share.Allow, c.Share.SourceDeny
	c.Flatten()
	for _, a := range legacyAllow {
		if !slices.Contains(c.Share.Allow, a) {
			c.Share.Allow = append(c.Share.Allow, a)
		}
	}
	for _, r := range legacySD {
		if !slices.Contains(c.Share.SourceDeny, r) {
			c.Share.SourceDeny = append(c.Share.SourceDeny, r)
		}
	}
	c.Fold()
}

// Fold writes edits made through Share.Allow and Share.SourceDeny back into
// the projects: an allow rule that is no member becomes a project of its own
// (named after the folder or chat grouping), and a member whose allow rule is
// gone leaves its project. A project emptied that way is removed; a project
// the user created empty stays. It also migrates a configuration written
// before projects existed.
func (c *Config) Fold() {
	inAllow := map[string]bool{}
	for _, a := range c.Share.Allow {
		inAllow[cleanMember(a)] = true
	}
	kept := c.Projects[:0:0]
	for _, p := range c.Projects {
		had := len(p.Members) > 0
		var ms []Member
		for _, m := range p.Members {
			if inAllow[cleanMember(m.Path)] {
				m.ExcludeSources = nil
				ms = append(ms, m)
			}
		}
		p.Members = ms
		if had && len(ms) == 0 {
			continue
		}
		kept = append(kept, p)
	}
	c.Projects = kept
	for _, a := range c.Share.Allow {
		a = strings.TrimSpace(a)
		if a == "" || c.memberIndex(a) != nil {
			continue
		}
		name := c.uniqueName(projectNameFor(a))
		c.Projects = append(c.Projects, Project{ID: idFor(a), Name: name, Members: []Member{{Path: a}}})
	}
	var leftover []string
	for _, r := range c.Share.SourceDeny {
		src, pat, ok := selection.SplitSourceRule(r)
		if !ok {
			continue
		}
		if m := c.memberIndex(pat); m != nil {
			if !slices.Contains(m.ExcludeSources, src) {
				m.ExcludeSources = append(m.ExcludeSources, src)
			}
			continue
		}
		leftover = append(leftover, r)
	}
	c.Share.SourceDeny = leftover
	c.ensureUniqueIDs()
	c.Flatten()
}

// memberIndex returns the member with this path, or nil.
func (c *Config) memberIndex(p string) *Member {
	want := cleanMember(p)
	for i := range c.Projects {
		for j := range c.Projects[i].Members {
			if cleanMember(c.Projects[i].Members[j].Path) == want {
				return &c.Projects[i].Members[j]
			}
		}
	}
	return nil
}

// ProjectIndex returns the index of the project whose ID or name (case
// insensitive) is ref, or -1.
func (c *Config) ProjectIndex(ref string) int {
	ref = strings.TrimSpace(ref)
	for i, p := range c.Projects {
		if p.ID == ref {
			return i
		}
	}
	for i, p := range c.Projects {
		if strings.EqualFold(p.Name, ref) {
			return i
		}
	}
	return -1
}

// ProjectOfMember returns the index of the project that has this member, or -1.
func (c *Config) ProjectOfMember(p string) int {
	want := cleanMember(p)
	for i, pr := range c.Projects {
		for _, m := range pr.Members {
			if cleanMember(m.Path) == want {
				return i
			}
		}
	}
	return -1
}

// CreateProject adds an empty project.
func (c *Config) CreateProject(name string) (Project, error) {
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return Project{}, err
	}
	if c.ProjectIndex(name) >= 0 {
		return Project{}, fmt.Errorf("a project named %q already exists", name)
	}
	p := Project{ID: newID(), Name: name}
	c.Projects = append(c.Projects, p)
	return p, nil
}

// RenameProject renames a project; its ID stays. Renaming to the name of
// another project is refused: merge them instead.
func (c *Config) RenameProject(ref, name string) error {
	i := c.ProjectIndex(ref)
	if i < 0 {
		return fmt.Errorf("no project named %q", ref)
	}
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return err
	}
	if j := c.ProjectIndex(name); j >= 0 && j != i {
		return fmt.Errorf("a project named %q already exists; merge into it instead", name)
	}
	c.Projects[i].Name = name
	return nil
}

// MergeProjects moves every member and work-item prefix of from into into and
// removes from. into keeps its ID and name.
func (c *Config) MergeProjects(from, into string) error {
	fi, ti := c.ProjectIndex(from), c.ProjectIndex(into)
	switch {
	case fi < 0:
		return fmt.Errorf("no project named %q", from)
	case ti < 0:
		return fmt.Errorf("no project named %q", into)
	case fi == ti:
		return fmt.Errorf("cannot merge %q into itself", from)
	}
	c.Projects[ti].Members = append(c.Projects[ti].Members, c.Projects[fi].Members...)
	for _, w := range c.Projects[fi].WorkItems {
		if !slices.Contains(c.Projects[ti].WorkItems, w) {
			c.Projects[ti].WorkItems = append(c.Projects[ti].WorkItems, w)
		}
	}
	c.Projects = slices.Delete(c.Projects, fi, fi+1)
	c.Flatten()
	return nil
}

// DeleteProject removes a project. Its members stop being shared.
func (c *Config) DeleteProject(ref string) (Project, error) {
	i := c.ProjectIndex(ref)
	if i < 0 {
		return Project{}, fmt.Errorf("no project named %q", ref)
	}
	p := c.Projects[i]
	c.Projects = slices.Delete(c.Projects, i, i+1)
	c.Flatten()
	return p, nil
}

// AddMember puts a folder, glob or chat key into a project, creating the
// project when no project has that name. A member of another project moves
// (a folder belongs to one project only).
func (c *Config) AddMember(ref, member string) (moved string, err error) {
	member = strings.TrimSpace(member)
	if member == "" {
		return "", fmt.Errorf("member is empty")
	}
	i := c.ProjectIndex(ref)
	if i < 0 {
		if _, err := c.CreateProject(ref); err != nil {
			return "", err
		}
		i = len(c.Projects) - 1
	}
	if j := c.ProjectOfMember(member); j >= 0 {
		if j == i {
			return "", nil
		}
		moved = c.Projects[j].Name
		var m Member
		c.Projects[j].Members = slices.DeleteFunc(c.Projects[j].Members, func(x Member) bool {
			if cleanMember(x.Path) == cleanMember(member) {
				m = x
				return true
			}
			return false
		})
		c.Projects[i].Members = append(c.Projects[i].Members, m)
	} else {
		c.Projects[i].Members = append(c.Projects[i].Members, Member{Path: member})
	}
	c.Flatten()
	return moved, nil
}

// RemoveMember takes a member out of a project; it stops being shared. The
// project stays, even when empty.
func (c *Config) RemoveMember(ref, member string) error {
	i := c.ProjectIndex(ref)
	if i < 0 {
		return fmt.Errorf("no project named %q", ref)
	}
	n := len(c.Projects[i].Members)
	c.Projects[i].Members = slices.DeleteFunc(c.Projects[i].Members, func(x Member) bool { return cleanMember(x.Path) == cleanMember(member) })
	if len(c.Projects[i].Members) == n {
		return fmt.Errorf("%s is not a member of %q", member, c.Projects[i].Name)
	}
	c.Flatten()
	return nil
}

// CloneProjects deep-copies projects, so an edited copy of a Config does not
// change the original.
func CloneProjects(ps []Project) []Project {
	if ps == nil {
		return nil
	}
	out := make([]Project, len(ps))
	for i, p := range ps {
		p.Members = slices.Clone(p.Members)
		for j := range p.Members {
			p.Members[j].ExcludeSources = slices.Clone(p.Members[j].ExcludeSources)
		}
		p.WorkItems = slices.Clone(p.WorkItems)
		out[i] = p
	}
	return out
}

func (c Config) validateProjects() error {
	ids, names, members := map[string]bool{}, map[string]string{}, map[string]string{}
	for _, p := range c.Projects {
		if p.ID == "" {
			return fmt.Errorf("config: project %q has no id", p.Name)
		}
		if ids[p.ID] {
			return fmt.Errorf("config: two projects share the id %q", p.ID)
		}
		ids[p.ID] = true
		if err := checkName(p.Name); err != nil {
			return fmt.Errorf("config: project %q: %w", p.ID, err)
		}
		low := strings.ToLower(strings.TrimSpace(p.Name))
		if other, ok := names[low]; ok {
			return fmt.Errorf("config: two projects are named %q (%s and %s)", p.Name, other, p.ID)
		}
		names[low] = p.ID
		for _, m := range p.Members {
			k := cleanMember(m.Path)
			if k == "" {
				return fmt.Errorf("config: project %q has an empty member", p.Name)
			}
			if other, ok := members[k]; ok {
				if other == p.Name {
					return fmt.Errorf("config: %s is listed twice in project %q", m.Path, p.Name)
				}
				return fmt.Errorf("config: %s is in two projects (%q and %q); a folder belongs to one project only", m.Path, other, p.Name)
			}
			members[k] = p.Name
			for _, s := range m.ExcludeSources {
				if _, _, ok := selection.SplitSourceRule(selection.SourceRule(s, "x")); !ok {
					return fmt.Errorf("config: project %q: %q is not a source name", p.Name, s)
				}
			}
		}
	}
	return nil
}

func checkName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("a project needs a name")
	case len(name) > 80:
		return fmt.Errorf("project names are at most 80 characters")
	case strings.ContainsAny(name, "\n\r\t"):
		return fmt.Errorf("project names are one line")
	}
	return nil
}

// cleanMember is the comparison form of a member: trimmed, without a
// trailing slash. Paths are compared as written; the matcher normalizes them.
func cleanMember(p string) string {
	p = strings.TrimSpace(p)
	if len(p) > 1 {
		p = strings.TrimRight(p, `/\`)
	}
	return p
}

// projectNameFor names a project made from one allow rule: the folder's base
// name, the chat grouping's key, or the glob as written.
func projectNameFor(member string) string {
	m := cleanMember(member)
	if pathutil.IsVirtualKey(m) || strings.ContainsAny(m, "*?[{") {
		return m
	}
	if b := path.Base(strings.ReplaceAll(m, `\`, "/")); b != "" && b != "." && b != "/" && b != "~" {
		return b
	}
	return m
}

func (c *Config) uniqueName(base string) string {
	if c.ProjectIndex(base) < 0 {
		return base
	}
	for n := 2; ; n++ {
		if name := fmt.Sprintf("%s (%d)", base, n); c.ProjectIndex(name) < 0 {
			return name
		}
	}
}

// idFor derives a project's ID from the member it was made from, so that a
// configuration migrated on load gets the same IDs until it is first saved.
func idFor(member string) string {
	h := sha256.Sum256([]byte(cleanMember(member)))
	return "p_" + hex.EncodeToString(h[:6])
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return "p_" + hex.EncodeToString(b[:])
}

func (c *Config) ensureUniqueIDs() {
	seen := map[string]bool{}
	for i := range c.Projects {
		for c.Projects[i].ID == "" || seen[c.Projects[i].ID] {
			c.Projects[i].ID = newID()
		}
		seen[c.Projects[i].ID] = true
	}
}
