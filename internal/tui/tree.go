package tui

import (
	"sort"
	"strings"
)

// treeNode is a folder in the directory view. A node can be a project in its
// own right and also have children (a repository with worktrees).
type treeNode struct {
	Name     string // path segment(s) shown, compacted
	Path     string // full path (the rule that selecting this node writes)
	Row      *Row   // set when the folder itself is a project
	Children []*treeNode
	Depth    int
}

// buildTree arranges folder projects into a compact directory tree. Chat
// buckets go into their own flat groups, one per product.
func buildTree(rows []Row) []*treeNode {
	root := &treeNode{}
	idx := map[string]*treeNode{"": root}
	var virtual []Row
	for i := range rows {
		r := rows[i]
		if r.Virtual {
			virtual = append(virtual, r)
			continue
		}
		parts := splitPath(r.Key)
		cur, path := root, ""
		for _, seg := range parts {
			path += "/" + seg
			n, ok := idx[path]
			if !ok {
				n = &treeNode{Name: seg, Path: path}
				idx[path] = n
				cur.Children = append(cur.Children, n)
			}
			cur = n
		}
		cur.Row = &rows[i]
	}
	var out []*treeNode
	for _, c := range root.Children {
		out = append(out, compact(c))
	}
	sortNodes(out)
	if len(virtual) > 0 {
		byProduct := map[string]*treeNode{}
		for i := range virtual {
			prod, _, _ := strings.Cut(virtual[i].Key, ":")
			g, ok := byProduct[prod]
			if !ok {
				g = &treeNode{Name: prod + ":", Path: prod + ":*"}
				byProduct[prod] = g
				out = append(out, g)
			}
			g.Children = append(g.Children, &treeNode{Name: strings.TrimPrefix(virtual[i].Key, prod+":"), Path: virtual[i].Key, Row: &virtual[i]})
		}
	}
	setDepth(out, 0)
	return out
}

func splitPath(p string) []string {
	var parts []string
	for _, s := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

// compact merges chains of folders that hold nothing else: a/b/c shows as one row.
func compact(n *treeNode) *treeNode {
	for len(n.Children) == 1 && n.Row == nil {
		c := n.Children[0]
		n = &treeNode{Name: n.Name + "/" + c.Name, Path: c.Path, Row: c.Row, Children: c.Children}
	}
	for i, c := range n.Children {
		n.Children[i] = compact(c)
	}
	sortNodes(n.Children)
	return n
}

func sortNodes(ns []*treeNode) {
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
}

func setDepth(ns []*treeNode, d int) {
	for _, n := range ns {
		n.Depth = d
		setDepth(n.Children, d+1)
	}
}

// descendants returns every project at or below n.
func (n *treeNode) descendants() []*Row {
	var out []*Row
	if n.Row != nil {
		out = append(out, n.Row)
	}
	for _, c := range n.Children {
		out = append(out, c.descendants()...)
	}
	return out
}

// state aggregates the share state of the projects at or below n.
func (n *treeNode) state() ShareState {
	rs := n.descendants()
	if len(rs) == 0 {
		return NotShared
	}
	shared, denied, partial := 0, 0, 0
	for _, r := range rs {
		switch r.State {
		case Shared:
			shared++
		case Denied:
			denied++
		case Partial:
			partial++
		}
	}
	switch {
	case shared == len(rs):
		return Shared
	case shared+partial > 0:
		return Partial
	case denied == len(rs):
		return Denied
	}
	return NotShared
}

// flatten lists the visible rows of the tree given which folders are expanded.
func flatten(ns []*treeNode, expanded map[string]bool) []*treeNode {
	var out []*treeNode
	for _, n := range ns {
		out = append(out, n)
		if len(n.Children) > 0 && expanded[n.Path] {
			out = append(out, flatten(n.Children, expanded)...)
		}
	}
	return out
}
