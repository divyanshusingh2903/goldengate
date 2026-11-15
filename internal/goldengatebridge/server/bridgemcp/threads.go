package bridgemcp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/threads"
)

const (
	// maxThreadSessions is how many sessions (newest first) threads are built
	// from in one call.
	maxThreadSessions = 500
	// maxActiveThreads caps the threads get_project_context leads with.
	maxActiveThreads = 10
	// maxThreadNarrative is the total narrative text get_work_item returns in
	// full, newest session first; later sessions carry their abstract.
	maxThreadNarrative = 8000
	asOfNote           = "state as of the session that last touched it, not live"
)

// ThreadItemView is one issue or pull request of a thread.
type ThreadItemView struct {
	Kind       string   `json:"kind"`
	Provider   string   `json:"provider,omitempty"`
	Key        string   `json:"key"`
	Title      string   `json:"title,omitempty"`
	URL        string   `json:"url,omitempty"`
	Status     string   `json:"status"`
	Confirmed  bool     `json:"confirmed"`
	RawState   string   `json:"raw_state,omitempty"`
	ObservedAt string   `json:"observed_at"`
	Evidence   []string `json:"evidence"`
}

// ThreadSessionRef names a session of a thread.
type ThreadSessionRef struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
}

// ThreadView is one piece of work in a list: the item it is about, what
// state it is in, who touched it and where to read more.
type ThreadView struct {
	ID       string           `json:"id"`   // pass to get_work_item as key
	Item     ThreadItemView   `json:"item"` // the issue or pull request the thread is named after
	Related  []ThreadItemView `json:"related,omitempty"`
	Summary  string           `json:"summary,omitempty"` // one or two lines from the latest session
	Sources  []string         `json:"sources"`           // the tools that touched it
	Sessions int              `json:"sessions"`
	Latest   ThreadSessionRef `json:"latest_session"`
	AsOf     string           `json:"as_of_note"`
}

func threadItem(it model.WorkItem) ThreadItemView {
	v := ThreadItemView{Kind: it.Kind, Provider: it.Provider, Key: it.Key, Title: it.Title, URL: it.URL, Status: it.Status,
		Confirmed: model.IsEvidenceBacked(it.Status), RawState: it.RawState, ObservedAt: ts(it.ObservedAt), Evidence: []string{}}
	for _, e := range it.Evidence {
		if !slices.Contains(v.Evidence, e.Via) {
			v.Evidence = append(v.Evidence, e.Via)
		}
	}
	return v
}

// sessionKey indexes records by source and id.
func sessionKey(source, id string) string { return source + "\x00" + id }

// buildThreads groups stored sessions into threads and indexes the records.
func buildThreads(recs []store.SessionRecord) ([]threads.Thread, map[string]store.SessionRecord) {
	in := make([]threads.Session, 0, len(recs))
	by := make(map[string]store.SessionRecord, len(recs))
	for _, r := range recs {
		s := threads.FromPayload(&r.Payload)
		if r.ProjectID != "" {
			s.ProjectID = r.ProjectID
		}
		s.ProjectName = projectName(r)
		in = append(in, s)
		by[sessionKey(r.Payload.Source, r.Payload.SessionID)] = r
	}
	return threads.Build(in), by
}

func threadView(th threads.Thread, by map[string]store.SessionRecord) ThreadView {
	v := ThreadView{ID: th.Primary.Key, Item: threadItem(th.Primary), Sources: th.Sources, Sessions: len(th.Sessions), AsOf: asOfNote}
	for _, it := range th.Items[1:] {
		v.Related = append(v.Related, threadItem(it))
	}
	if len(th.Sessions) > 0 {
		m := th.Sessions[0]
		v.Latest = ThreadSessionRef{ID: m.ID, Source: m.Source, Title: m.Title, UpdatedAt: ts(m.UpdatedAt)}
		if r, ok := by[sessionKey(m.Source, m.ID)]; ok {
			v.Summary = threadSummary(r)
		}
	}
	return v
}

// threadSummary is one or two lines about where a thread stands, from its
// latest session: the abstract, else what the rules recorded.
func threadSummary(r store.SessionRecord) string {
	sm := r.Payload.Summary
	if sm == nil {
		return ""
	}
	if a := sm.AbstractText(); a != "" {
		return a
	}
	if o := sm.OutcomeList(); len(o) > 0 {
		return strings.Join(o[:min(len(o), 3)], "; ")
	}
	return ""
}

// threadActive reports whether a thread still has work open, as far as the
// sessions show. Mentions prove nothing either way, so a thread is finished
// only on evidence: an issue a session moved to done, or every pull request
// merged or closed.
func threadActive(th threads.Thread) bool {
	prs, prsOpen, issuesDone := 0, 0, 0
	for _, it := range th.Items {
		switch {
		case it.Kind == model.KindPR:
			prs++
			if unfinished(it) {
				prsOpen++
			}
		case model.IsEvidenceBacked(it.Status):
			if unfinished(it) {
				return true // a session moved the issue to a state that is not done
			}
			issuesDone++
		}
	}
	if prs > 0 {
		return prsOpen > 0
	}
	return issuesDone == 0
}

func itemLine(now time.Time, v ThreadItemView) string {
	obs, _ := time.Parse(time.RFC3339, v.ObservedAt)
	conf := "confirmed by " + strings.Join(v.Evidence, "+")
	if !v.Confirmed {
		conf = "NOT confirmed: only mentioned"
	}
	line := fmt.Sprintf("%s %s [%s] %s (%s; %s)", v.Kind, clean(v.Key), v.Status, clean(v.Title), ago(now, obs), conf)
	if v.RawState != "" {
		line += " tracker state: " + clean(v.RawState)
	}
	if v.URL != "" {
		line += " " + clean(v.URL)
	}
	return line
}

// threadLines renders a thread in four lines or fewer plus one per related item.
func threadLines(now time.Time, v ThreadView) []string {
	l := []string{"- " + itemLine(now, v.Item)}
	for _, r := range v.Related {
		l = append(l, "    with "+itemLine(now, r))
	}
	if v.Summary != "" {
		l = append(l, "    summary: "+clean(v.Summary))
	}
	latest, _ := time.Parse(time.RFC3339, v.Latest.UpdatedAt)
	l = append(l, fmt.Sprintf("    touched by %s in %d session(s); latest: [%s] %s (%s) id=%s",
		clean(strings.Join(v.Sources, ", ")), v.Sessions, clean(v.Latest.Source), clean(v.Latest.Title), ago(now, latest), clean(v.Latest.ID)))
	return l
}

// --- get_work_item ---

// GetWorkItemIn is the input of get_work_item.
type GetWorkItemIn struct {
	Key     string `json:"key" jsonschema:"the issue or pull request: a key from another tool (linear:ENG-12, owner/repo#7), the bare tracker key (ENG-12), or its URL"`
	Project string `json:"project,omitempty" jsonschema:"only look in this project: its name or id, as shown by list_projects"`
}

// StatusEvent is one observation of an item's status in one session.
type StatusEvent struct {
	At        string   `json:"at"`
	Key       string   `json:"key"`
	Status    string   `json:"status"`
	Confirmed bool     `json:"confirmed"`
	Evidence  []string `json:"evidence"`
	RawState  string   `json:"raw_state,omitempty"`
	SessionID string   `json:"session_id"`
	Source    string   `json:"source"`
}

// ThreadSessionView is a session of a thread with what it did to the items.
type ThreadSessionView struct {
	SessionView
	Touches []threads.Touch `json:"touches"`
}

// GetWorkItemOut is the output of get_work_item.
type GetWorkItemOut struct {
	Notice   string              `json:"notice"`
	Thread   ThreadView          `json:"thread"`
	Sessions []ThreadSessionView `json:"sessions"` // newest first
	History  []StatusEvent       `json:"status_history"`
	// NarrativesCut is set when older sessions carry only their abstract.
	NarrativesCut bool `json:"narratives_cut,omitempty"`
}

// matchKey reports how well an item answers what the assistant asked for:
// 2 for its key or URL, 1 for the key without its provider or repository
// ("ENG-12", "#7", "!7"), 0 for no match.
func matchKey(it model.WorkItem, want string) int {
	want = strings.ToLower(strings.TrimSpace(want))
	key := strings.ToLower(it.Key)
	if want == "" {
		return 0
	}
	if want == key || (it.URL != "" && strings.TrimSuffix(want, "/") == strings.ToLower(it.URL)) || want == it.Kind+":"+key {
		return 2
	}
	if _, bare, ok := strings.Cut(key, ":"); ok && want == bare {
		return 2 // the provider prefix is ours, not the tracker's
	}
	if i := strings.LastIndexAny(key, "#!"); i >= 0 && (want == key[i:] || strings.HasSuffix(key, "/"+want)) {
		return 1
	}
	return 0
}

func (s *srv) getWorkItem(ctx context.Context, req *mcp.CallToolRequest, in GetWorkItemIn) (*mcp.CallToolResult, GetWorkItemOut, error) {
	pid := s.projectID(ctx, in.Project)
	s.audit(ctx, req, "get_work_item", pid)
	if strings.TrimSpace(in.Key) == "" {
		return nil, GetWorkItemOut{}, fmt.Errorf("key is required")
	}
	recs, err := s.Store.Sessions(ctx, store.SessionQuery{Project: pid, Limit: maxThreadSessions})
	if err != nil {
		return nil, GetWorkItemOut{}, fmt.Errorf("could not read sessions")
	}
	all, by := buildThreads(recs)
	var found []threads.Thread
	best := 0
	for _, th := range all {
		m := 0
		for _, it := range th.Items {
			m = max(m, matchKey(it, in.Key))
		}
		switch {
		case m > best:
			best, found = m, []threads.Thread{th}
		case m == best && m > 0:
			found = append(found, th)
		}
	}
	switch {
	case len(found) == 0:
		return nil, GetWorkItemOut{}, fmt.Errorf("no such work item: use a key from list_issues, list_pull_requests or get_project_context")
	case len(found) > 1:
		return nil, GetWorkItemOut{}, fmt.Errorf("%d work items match: pass the full key from list_issues or list_pull_requests, or name the project", len(found))
	}
	th := found[0]
	out := GetWorkItemOut{Notice: notice, Thread: threadView(th, by), Sessions: []ThreadSessionView{}, History: []StatusEvent{}}
	now := s.Now()
	lines := []string{"Work item:"}
	lines = append(lines, threadLines(now, out.Thread)...)
	lines = append(lines, "", "Sessions, newest first:")
	budget := maxThreadNarrative
	for _, m := range th.Sessions {
		r, ok := by[sessionKey(m.Source, m.ID)]
		if !ok {
			continue
		}
		v := viewOf(r, true)
		if sm := r.Payload.Summary; sm != nil && sm.Narrative != "" {
			if n := len(sm.Narrative); n <= budget {
				v.Narrative, budget = sm.Narrative, budget-n
			} else {
				out.NarrativesCut, budget = true, 0 // older sessions keep their abstract
			}
		}
		out.Sessions = append(out.Sessions, ThreadSessionView{SessionView: v, Touches: m.Touches})
		lines = append(lines, sessionLines(now, v, true)...)
		for _, t := range m.Touches {
			how := t.Status
			if t.Branch {
				how += ", on its branch"
			}
			lines = append(lines, fmt.Sprintf("    touched: %s %s (%s)", t.Kind, clean(t.Key), how))
		}
		for _, it := range r.Payload.WorkItems {
			for _, t := range m.Touches {
				if t.Kind != it.Kind || t.Status != it.Status || !sameItem(t.Key, it.Key) {
					continue
				}
				iv := threadItem(it)
				out.History = append(out.History, StatusEvent{At: iv.ObservedAt, Key: t.Key, Status: it.Status, Confirmed: iv.Confirmed,
					Evidence: iv.Evidence, RawState: it.RawState, SessionID: m.ID, Source: m.Source})
				break
			}
		}
	}
	slices.SortStableFunc(out.History, func(a, b StatusEvent) int { return strings.Compare(a.At, b.At) })
	lines = append(lines, "", "Status history, oldest first (each line is what one session saw, not live state):")
	for _, h := range out.History {
		at, _ := time.Parse(time.RFC3339, h.At)
		conf := "confirmed by " + strings.Join(h.Evidence, "+")
		if !h.Confirmed {
			conf = "not confirmed: " + strings.Join(h.Evidence, "+")
		}
		l := fmt.Sprintf("- %s %s [%s] %s, [%s] id=%s", ago(now, at), clean(h.Key), h.Status, conf, clean(h.Source), clean(h.SessionID))
		if h.RawState != "" {
			l += " tracker state: " + clean(h.RawState)
		}
		lines = append(lines, l)
	}
	if out.NarrativesCut {
		lines = append(lines, "", "Older sessions show their abstract only; get_session has each full narrative.")
	}
	return result(lines), out, nil
}

// sameItem reports whether a session's own key is the thread's key for it:
// equal, or the short form ("app#7") of the full one ("example/app#7").
func sameItem(threadKey, own string) bool {
	return threadKey == own || strings.HasSuffix(threadKey, "/"+own) || strings.HasSuffix(threadKey, "/"+strings.TrimPrefix(own, "github:")) ||
		strings.HasSuffix(threadKey, "/"+strings.TrimPrefix(own, "gitlab:"))
}
