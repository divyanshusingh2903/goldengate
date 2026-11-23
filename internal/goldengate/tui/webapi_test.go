package tui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/webui"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/webui/webuitest"
)

// The web API is a thin layer over tui.Backend. This drives the terminal
// UI's own fake backend through every endpoint and checks that each Backend
// method is reached with what the page sent, and that its answer comes back.
func TestEveryBackendMethodIsReachableOverTheWebAPI(t *testing.T) {
	be := tui.NewFake(t.TempDir())
	srv, err := webui.New(be, webui.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := webuitest.Start(t, srv)
	c.Launch()
	q := url.QueryEscape

	// Snapshot
	var snap tui.Snapshot
	c.JSON("GET", "/api/snapshot", nil, &snap)
	if len(snap.Rows) != 8 || snap.Rows[0].Label != "~/code/pharmfindr" || snap.Server == "" {
		t.Fatalf("snapshot: %+v", snap)
	}
	// Preview
	var pv tui.Preview
	c.JSON("GET", "/api/preview?limit=5&key="+q("/home/u/code/pharmfindr"), nil, &pv)
	if pv.Key != "/home/u/code/pharmfindr" || len(pv.Items) == 0 || pv.Items[0].Payload.SessionID == "" {
		t.Fatalf("preview: %+v", pv)
	}
	// Apply, with the opaque remote handle kept on the server side
	var applied struct {
		Message string
		Remote  *struct {
			Count int
			ID    string
		}
	}
	c.JSON("POST", "/api/apply", tui.Action{Kind: tui.ActRemove, Keys: []string{"/home/u/code/forge"}}, &applied)
	if applied.Remote == nil || applied.Remote.ID == "" || applied.Remote.Count != 1 {
		t.Fatalf("apply: %+v", applied)
	}
	// DeleteRemote gets the backend's own handle back, once
	var msg struct{ Message string }
	c.JSON("POST", "/api/delete-remote", map[string]string{"ID": applied.Remote.ID}, &msg)
	if res, _ := c.Do("POST", "/api/delete-remote", map[string]string{"ID": applied.Remote.ID}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("a remote handle is used once: %d", res.StatusCode)
	}
	// Undo
	c.JSON("POST", "/api/undo", struct{}{}, &msg)
	// Plan
	var plan tui.PlanInfo
	c.JSON("GET", "/api/plan?resummarize=true", nil, &plan)
	if plan.Sessions == 0 || plan.Server == "" {
		t.Fatalf("plan: %+v", plan)
	}
	// Sync, with its events as a stream
	var run struct{ ID string }
	c.JSON("POST", "/api/sync", map[string]bool{"Dry": true}, &run)
	res, stream := c.Do("GET", "/api/sync/"+run.ID+"/events", nil, nil)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") ||
		strings.Count(string(stream), "event: sync") < 2 || !strings.HasSuffix(strings.TrimSpace(string(stream)), `data: {"error":""}`) || !strings.Contains(string(stream), "event: end") {
		t.Fatalf("sync events: %d %s", res.StatusCode, stream)
	}
	if res, _ := c.Do("GET", "/api/sync/nope/events", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run: %d", res.StatusCode)
	}
	c.JSON("POST", "/api/sync/"+run.ID+"/cancel", struct{}{}, nil)
	// Server, DeleteServerProject
	var info tui.ServerInfo
	c.JSON("GET", "/api/server", nil, &info)
	if !info.Healthy || len(info.Projects) != 2 {
		t.Fatalf("server: %+v", info)
	}
	c.JSON("POST", "/api/server/delete-project", map[string]string{"Ref": "p_forge1"}, nil)
	// Settings, SaveSettings
	var set tui.Settings
	c.JSON("GET", "/api/settings", nil, &set)
	set.Level = "titles-only"
	c.JSON("POST", "/api/settings", set, nil)
	// SetDaemon: the answer carries the warning; the settings then show it on
	var dres map[string]string
	c.JSON("POST", "/api/daemon", map[string]bool{"On": true}, &dres)
	if _, ok := dres["warning"]; !ok {
		t.Fatalf("daemon answer: %v", dres)
	}
	c.JSON("GET", "/api/settings", nil, &set)
	if set.Daemon.Mode != "on" || !set.Daemon.Running {
		t.Fatalf("settings after turning the daemon on: %+v", set.Daemon)
	}
	c.JSON("POST", "/api/daemon", map[string]bool{"On": false}, &dres)
	c.JSON("GET", "/api/settings", nil, &set)
	if set.Daemon.Mode != "off" {
		t.Fatalf("settings after turning it off: %+v", set.Daemon)
	}
	// SetSummarizerKey: the key goes in and does not come back
	res, body := c.Do("POST", "/api/summarizer/key", map[string]string{"Provider": "anthropic", "Key": "sk-TOPSECRET-KEY"}, nil)
	if res.StatusCode != 200 || strings.Contains(string(body), "TOPSECRET") || !strings.Contains(string(body), `"set":true`) {
		t.Fatalf("set key: %d %s", res.StatusCode, body)
	}
	// TestSummarizer, SummarizerModels
	c.JSON("POST", "/api/summarizer/test", struct{}{}, &msg)
	var models []string
	c.JSON("GET", "/api/summarizer/models?provider=anthropic", nil, &models)
	if len(models) != 2 {
		t.Fatalf("models: %v", models)
	}
	// Detect
	var tools []tui.ToolStatus
	c.JSON("GET", "/api/detect", nil, &tools)
	// Init: the assistant token is shown once, the admin secret never comes back
	res, body = c.Do("POST", "/api/init", tui.InitParams{ServerURL: "https://bridge.example.com", AdminToken: "ADMIN-TOPSECRET"}, nil)
	if res.StatusCode != 200 || !strings.Contains(string(body), "ggb_read_EXAMPLE") || strings.Contains(string(body), "ADMIN-TOPSECRET") {
		t.Fatalf("init: %d %s", res.StatusCode, body)
	}
	// Inbox, InboxDecide
	var inbox tui.Inbox
	c.JSON("GET", "/api/inbox", nil, &inbox)
	if len(inbox.Pending) != 2 || len(inbox.Offers) != 1 {
		t.Fatalf("inbox: %+v", inbox)
	}
	c.JSON("POST", "/api/inbox/decide", map[string]any{"Items": inbox.Pending[:1], "Decision": tui.InboxApprove}, nil)
	// EditBegin, EditFinish: the form's text travels, the file stays here
	var form struct{ ID, Text string }
	c.JSON("POST", "/api/edit/begin", map[string]any{"Item": inbox.Pending[1]}, &form)
	if form.ID == "" || !strings.Contains(form.Text, "form for "+inbox.Pending[1].ID) || strings.Contains(form.Text, t.TempDir()) {
		t.Fatalf("edit form: %+v", form)
	}
	c.JSON("POST", "/api/edit/finish", map[string]any{"ID": form.ID, "Text": "title: My edited title\n"}, &msg)
	if !strings.Contains(msg.Message, "saved your version") {
		t.Fatalf("edit finish: %+v", msg)
	}
	if res, _ := c.Do("POST", "/api/edit/finish", map[string]any{"ID": form.ID, "Text": "x"}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("an edit is finished once: %d", res.StatusCode)
	}
	c.JSON("POST", "/api/edit/begin", map[string]any{"Item": inbox.Pending[1]}, &form)
	c.JSON("POST", "/api/edit/finish", map[string]any{"ID": form.ID, "Cancel": true}, nil)
	// OfferDecide
	c.JSON("POST", "/api/offers/decide", map[string]any{"ID": "o_1a2b3c4d", "Accept": true}, &msg)
	// Receipts
	var receipts []tui.Receipt
	c.JSON("GET", "/api/receipts", nil, &receipts)
	if len(receipts) != 1 || receipts[0].Items[0].Why != "rule r_1" {
		t.Fatalf("receipts: %+v", receipts)
	}

	// Rules, AddRule, RemoveRule, SetTrust
	var rs []tui.ProjectRules
	c.JSON("GET", "/api/rules", nil, &rs)
	if len(rs) != 1 || rs[0].Name != "Forge" || len(rs[0].Rules) != 1 {
		t.Fatalf("rules: %+v", rs)
	}
	c.JSON("POST", "/api/rules/add", map[string]any{"Project": "Forge", "Rule": tui.RuleInfo{Action: "exclude", Branch: "wip/*"}}, &msg)
	if !strings.Contains(msg.Message, "added rule r_2") {
		t.Fatalf("add rule: %+v", msg)
	}
	c.JSON("POST", "/api/rules/trust", map[string]string{"Project": "Forge", "Trust": "suggest"}, &msg)
	c.JSON("POST", "/api/rules/remove", map[string]string{"Project": "Forge", "ID": "r_1"}, &msg)
	c.JSON("GET", "/api/rules", nil, &rs)
	if rs[0].Trust != "suggest" || len(rs[0].Rules) != 1 || rs[0].Rules[0].Branch != "wip/*" {
		t.Fatalf("rules after changes: %+v", rs)
	}
	if res, _ := c.Do("POST", "/api/rules/remove", map[string]string{"Project": "Nope", "ID": "r_1"}, nil); res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("removing from a missing project: %d", res.StatusCode)
	}

	// Agents: nothing without the admin secret, and a wrong one is not kept
	if res, body := c.Do("GET", "/api/agents", nil, nil); res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "admin_required") {
		t.Fatalf("agents without the admin secret: %d %s", res.StatusCode, body)
	}
	if res, _ := c.Do("POST", "/api/admin", map[string]string{"Secret": "wrong"}, nil); res.StatusCode == 200 {
		t.Fatal("a wrong admin secret was accepted")
	}
	if res, _ := c.Do("GET", "/api/agents", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("a wrong admin secret was kept")
	}
	res, body = c.Do("POST", "/api/admin", map[string]string{"Secret": tui.FakeAdmin}, nil)
	if res.StatusCode != 200 || strings.Contains(string(body), tui.FakeAdmin) {
		t.Fatalf("admin: %d %s", res.StatusCode, body)
	}
	var agents tui.AgentsInfo
	c.JSON("GET", "/api/agents", nil, &agents)
	if len(agents.Agents) != 3 {
		t.Fatalf("agents: %+v", agents)
	}
	c.JSON("POST", "/api/agents/grants", map[string]any{"Changes": []tui.GrantChange{{AgentID: "agt_gpt", ProjectID: "p_forge", Grant: true}}}, nil)
	var view tui.AgentView
	c.JSON("GET", "/api/agents/view?agent=agt_gpt", nil, &view)
	if view.Agent.ID != "agt_gpt" {
		t.Fatalf("view: %+v", view)
	}
	c.JSON("POST", "/api/agents/revoke-token", map[string]string{"TokenID": "tok_bbbb2222"}, nil)
	c.JSON("POST", "/api/admin/forget", struct{}{}, nil)
	if res, _ := c.Do("GET", "/api/agents", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("forgetting the admin secret must stop agent calls")
	}

	// what the backend saw
	r := be.Recorded()
	if len(r.Actions) != 1 || r.Actions[0].Kind != tui.ActRemove || r.Undos != 1 || len(r.RemoteDels) != 1 {
		t.Errorf("sharing calls: %+v", r)
	}
	if h, ok := r.RemoteDels[0].([]string); !ok || h[0] != "opaque-handle" {
		t.Errorf("DeleteRemote got the backend's own handle: %#v", r.RemoteDels[0])
	}
	if len(r.PlanOpts) == 0 || !r.PlanOpts[0].Resummarize || len(r.Syncs) != 1 || !r.Syncs[0] {
		t.Errorf("plan and sync options: %+v %+v", r.PlanOpts, r.Syncs)
	}
	if len(r.ServerDels) != 1 || r.ServerDels[0] != "p_forge1" || len(r.Saved) != 1 || r.Saved[0].Level != "titles-only" {
		t.Errorf("server and settings: %+v", r)
	}
	if r.Keys["anthropic"] != "sk-TOPSECRET-KEY" || r.Tests != 1 || len(r.Inits) != 1 || r.Inits[0].AdminToken != "ADMIN-TOPSECRET" {
		t.Errorf("keys, test and init: %+v", r)
	}
	if strings.Join(r.InboxDecs, " ") != "approve:"+inbox.Pending[0].ID || strings.Join(r.OfferDecs, " ") != "accept:o_1a2b3c4d" {
		t.Errorf("inbox and offers: %v %v", r.InboxDecs, r.OfferDecs)
	}
	if len(r.EditBegun) != 2 || len(r.EditFinished) != 2 || !strings.HasSuffix(r.EditFinished[0], ".txt") || !strings.HasSuffix(r.EditFinished[1], "@") {
		t.Errorf("edits (one saved, one cancelled): %v %v", r.EditBegun, r.EditFinished)
	}
	if !r.Grants["p_forge"] || r.AdminSecrets[len(r.AdminSecrets)-1] != tui.FakeAdmin {
		t.Errorf("agents calls carried the admin secret: %+v", r)
	}
}
