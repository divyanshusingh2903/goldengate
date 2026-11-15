// Package threads groups sessions from different tools into threads: the
// sessions that share an issue or a pull request, which is one piece of work.
//
// It works only on what a payload already carries (work items with their
// evidence), so the laptop and the server compute the same threads, and it
// never changes a status: items seen in several sessions are combined with
// workitems.Merge, where a mention never outranks evidence.
package threads

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// Session is what Build needs to know about one session.
type Session struct {
	Source      string
	ID          string
	ProjectID   string // the project the session is filed under
	ProjectName string
	Title       string
	Branch      string
	UpdatedAt   time.Time
	Items       []model.WorkItem
}

// FromPayload reads a Session out of a payload.
func FromPayload(p *model.SessionPayload) Session {
	s := Session{
		Source: p.Source, ID: p.SessionID, ProjectID: p.ProjectRef(), ProjectName: p.ProjectLabel(),
		Title: p.TitleOnly, UpdatedAt: p.UpdatedAt, Items: p.WorkItems,
	}
	if p.Summary != nil {
		s.Title, s.Branch = p.Summary.Title, p.Summary.GitBranch
	}
	return s
}

// Touch is how one session relates to one item of a thread.
type Touch struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Status string `json:"status"` // as of this session; "mentioned" unless the session has evidence
	// Branch is set when the session ran on a branch named after the issue.
	Branch bool `json:"branch,omitempty"`
}

// Member is one session of a thread.
type Member struct {
	Source      string    `json:"source"`
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name,omitempty"`
	Title       string    `json:"title,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Touches     []Touch   `json:"touches"`
}

// Thread is one piece of work: its issues and pull requests, and every
// session that touched any of them.
type Thread struct {
	// ID is "<kind>:<key>" of the primary item. It changes only if a more
	// fitting primary appears (an issue for a thread that had only a PR).
	ID string `json:"id"`
	// Primary is the item the thread is named after: a tracker issue before a
	// GitHub issue before a pull request, the earliest seen first.
	Primary model.WorkItem `json:"primary"`
	// Items are the thread's issues and pull requests, Primary first, each
	// combined across sessions with the work-item merge rules.
	Items []model.WorkItem `json:"items"`
	// Sessions are newest first.
	Sessions     []Member  `json:"sessions"`
	Sources      []string  `json:"sources"` // sorted, distinct
	FirstSeen    time.Time `json:"first_seen"`
	LastActivity time.Time `json:"last_activity"`
}

// node is one issue or pull request, across every session that has it.
type node struct {
	id       string
	item     model.WorkItem
	first    time.Time
	parent   *node
	sessions []int // indexes into the sessions slice, in input order
}

func (n *node) root() *node {
	for n.parent != nil {
		if n.parent.parent != nil {
			n.parent = n.parent.parent
		}
		n = n.parent
	}
	return n
}

func union(a, b *node) {
	if ra, rb := a.root(), b.root(); ra != rb {
		rb.parent = ra
	}
}

func anchors(it model.WorkItem) bool { return it.Kind == model.KindIssue || it.Kind == model.KindPR }

// hosted reports whether an item lives in a repository (GitHub, GitLab)
// rather than in a tracker with keys of its own.
func hosted(it model.WorkItem) bool { return it.Provider == "github" || it.Provider == "gitlab" }

// repoOf splits a repository key ("owner/repo#7", "github:owner/repo#7",
// "repo#7", GitLab's "group/project!7") into its repository and number. ok
// is false for tracker keys.
func repoOf(it model.WorkItem) (repo, num string, ok bool) {
	if !hosted(it) {
		return "", "", false
	}
	key := strings.TrimPrefix(it.Key, it.Provider+":")
	i := strings.LastIndexAny(key, "#!")
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// Build groups sessions into threads, most recently active first.
//
// Two sessions are in one thread when they have the same issue or the same
// pull request, whatever the status: a chat that mentions LIN-412 and the
// coding session that worked on it belong together. An issue and a pull
// request are one thread when a session ties them: it ran on a branch named
// after the issue and has evidence about the pull request (it opened, changed
// or merged it, or the tool recorded the pull request for that session), or
// the pull request's title names the issue. A session that only mentions two
// issues never joins them; it appears in both threads.
//
// A GitHub or GitLab key without an owner ("app#7", written by a session that never saw
// the URL) is taken as the full key ("example/app#7") when exactly one full
// key of that kind and number exists in the same project.
func Build(sessions []Session) []Thread {
	// full repository keys per project, host, kind and number
	type slot struct{ project, provider, kind, num string }
	full := map[slot][]string{}
	for _, s := range sessions {
		for _, it := range s.Items {
			if repo, num, ok := repoOf(it); ok && anchors(it) && strings.Contains(repo, "/") {
				k := slot{s.ProjectID, it.Provider, it.Kind, num}
				if !slices.Contains(full[k], it.Key) {
					full[k] = append(full[k], it.Key)
				}
			}
		}
	}
	// canonical returns the node id and the key to show for an item.
	canonical := func(s Session, it model.WorkItem) (id, key string) {
		key = it.Key
		if repo, num, ok := repoOf(it); ok && !strings.Contains(repo, "/") {
			if c := full[slot{s.ProjectID, it.Provider, it.Kind, num}]; len(c) == 1 {
				key = c[0]
			} else {
				// no owner and nothing to match: the key means something
				// only inside its own project
				return it.Kind + "|" + s.ProjectID + "|" + key, key
			}
		}
		return it.Kind + "|" + key, key
	}

	order := make([]int, len(sessions)) // oldest first, so merges run in time order
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return sessions[a].UpdatedAt.Compare(sessions[b].UpdatedAt) })

	nodes := map[string]*node{}
	var all []*node
	type tie struct{ issues, prs []*node }
	for _, si := range order {
		s := sessions[si]
		var t tie
		for _, it := range s.Items {
			if !anchors(it) {
				continue
			}
			id, key := canonical(s, it)
			it.Key = key
			n := nodes[id]
			if n == nil {
				n = &node{id: id, item: it, first: it.ObservedAt}
				nodes[id] = n
				all = append(all, n)
			} else {
				n.item = workitems.Merge(n.item, it)
				if it.ObservedAt.Before(n.first) {
					n.first = it.ObservedAt
				}
			}
			if !slices.Contains(n.sessions, si) {
				n.sessions = append(n.sessions, si)
			}
			switch {
			case it.Kind == model.KindIssue && via(it, model.ViaBranch):
				t.issues = append(t.issues, n)
			case it.Kind == model.KindPR && (model.IsEvidenceBacked(it.Status) || via(it, model.ViaHarnessEvent)):
				t.prs = append(t.prs, n)
			}
		}
		for _, i := range t.issues {
			for _, p := range t.prs {
				union(i, p)
			}
		}
	}
	// a pull request whose title names a tracker issue
	for _, p := range all {
		if p.item.Kind != model.KindPR || p.item.Title == "" {
			continue
		}
		for _, i := range all {
			if i.item.Kind == model.KindIssue && !hosted(i.item) && namesKey(p.item.Title, i.item.Key) {
				union(i, p)
			}
		}
	}

	groups := map[*node][]*node{}
	var roots []*node
	for _, n := range all {
		r := n.root()
		if groups[r] == nil {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], n)
	}
	out := make([]Thread, 0, len(roots))
	for _, r := range roots {
		out = append(out, thread(groups[r], sessions, canonical))
	}
	slices.SortStableFunc(out, func(a, b Thread) int {
		return cmp.Or(b.LastActivity.Compare(a.LastActivity), cmp.Compare(a.ID, b.ID))
	})
	return out
}

func thread(ns []*node, sessions []Session, canonical func(Session, model.WorkItem) (string, string)) Thread {
	slices.SortStableFunc(ns, func(a, b *node) int {
		return cmp.Or(cmp.Compare(rank(a.item), rank(b.item)), a.first.Compare(b.first), cmp.Compare(a.id, b.id))
	})
	th := Thread{Primary: ns[0].item, ID: ns[0].item.Kind + ":" + ns[0].item.Key, FirstSeen: ns[0].first}
	in := map[string]bool{}
	var members []int
	for _, n := range ns {
		th.Items = append(th.Items, n.item)
		in[n.id] = true
		if n.first.Before(th.FirstSeen) {
			th.FirstSeen = n.first
		}
		for _, si := range n.sessions {
			if !slices.Contains(members, si) {
				members = append(members, si)
			}
		}
	}
	for _, si := range members {
		s := sessions[si]
		m := Member{Source: s.Source, ID: s.ID, ProjectID: s.ProjectID, ProjectName: s.ProjectName, Title: s.Title, UpdatedAt: s.UpdatedAt}
		for _, it := range s.Items {
			if !anchors(it) {
				continue
			}
			if id, key := canonical(s, it); in[id] {
				m.Touches = append(m.Touches, Touch{Kind: it.Kind, Key: key, Status: it.Status, Branch: via(it, model.ViaBranch)})
			}
		}
		slices.SortStableFunc(m.Touches, func(a, b Touch) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Key, b.Key)) })
		th.Sessions = append(th.Sessions, m)
		if !slices.Contains(th.Sources, s.Source) {
			th.Sources = append(th.Sources, s.Source)
		}
		if s.UpdatedAt.After(th.LastActivity) {
			th.LastActivity = s.UpdatedAt
		}
	}
	slices.Sort(th.Sources)
	slices.SortStableFunc(th.Sessions, func(a, b Member) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(a.Source, b.Source), cmp.Compare(a.ID, b.ID))
	})
	return th
}

// rank orders a thread's items: tracker issue, GitHub or GitLab issue, pull
// or merge request.
func rank(it model.WorkItem) int {
	switch {
	case it.Kind == model.KindIssue && !hosted(it):
		return 0
	case it.Kind == model.KindIssue:
		return 1
	}
	return 2
}

func via(it model.WorkItem, v string) bool {
	return slices.ContainsFunc(it.Evidence, func(e model.Evidence) bool { return e.Via == v })
}

var reWord = regexp.MustCompile(`[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*`)

// namesKey reports whether title contains the tracker key ("linear:LIN-412")
// as a whole word, in either case.
func namesKey(title, key string) bool {
	_, k, ok := strings.Cut(key, ":")
	if !ok || k == "" {
		return false
	}
	for _, w := range reWord.FindAllString(title, -1) {
		// "LIN-412" and "lin-412-fix" both name the issue; "LIN-4120" does not
		if strings.EqualFold(w, k) || (len(w) > len(k) && strings.EqualFold(w[:len(k)], k) && w[len(k)] == '-') {
			return true
		}
	}
	return false
}
