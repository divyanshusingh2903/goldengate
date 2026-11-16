package tui

// The web API's contract tests (webapi_test.go, package tui_test) drive the
// same fake backend as the terminal UI's tests. These exports exist only in
// test builds.

// FakeBackend is the test backend.
type FakeBackend = fakeBackend

// FakeAdmin is the admin secret the fake accepts.
const FakeAdmin = fakeAdmin

// NewFake returns the test backend with its sample data, an inbox, receipts,
// one offer, and edit forms written under editDir.
func NewFake(editDir string) *FakeBackend {
	f := newFake()
	f.inbox = sampleInbox()
	f.inbox.Offers = []RuleOffer{{ID: "o_1a2b3c4d", Question: "Share sessions from ~/code/app automatically?"}}
	f.receipts = []Receipt{{ID: "rc_0a1b2c3d", Items: []ReceiptItem{{Source: "codex", ID: "s-auto-1", Project: "Forge", Title: "Add health checks", Why: "rule r_1"}}}}
	f.editDir = editDir
	f.models = map[string][]string{"anthropic": {"model-a", "model-b"}}
	f.remote = &RemotePrompt{Count: 1, Handle: []string{"opaque-handle"}}
	return f
}

// Recorded is what the fake saw, for assertions.
type Recorded struct {
	Actions      []Action
	Undos        int
	RemoteDels   []any
	ServerDels   []string
	Saved        []Settings
	Inits        []InitParams
	Syncs        []bool
	PlanOpts     []SyncOpts
	InboxDecs    []string
	OfferDecs    []string
	EditBegun    []string
	EditFinished []string
	Keys         map[string]string
	Tests        int
	AdminSecrets []string
	Grants       map[string]bool // ChatGPT's
}

// Recorded returns a copy of what the fake recorded.
func (f *FakeBackend) Recorded() Recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := Recorded{Actions: f.actions, Undos: f.undos, RemoteDels: f.remoteDels, ServerDels: f.srvDels, Saved: f.saved, Inits: f.inits, Syncs: f.syncs,
		PlanOpts: f.planOpts, InboxDecs: f.inboxDecs, OfferDecs: f.offerDecs, EditBegun: f.editBegun, EditFinished: f.editFinished,
		Keys: f.keys, Tests: f.tests, AdminSecrets: f.ag.secrets}
	for _, a := range f.ag.info.Agents {
		if a.ID == "agt_gpt" {
			r.Grants = a.Grants
		}
	}
	return r
}
