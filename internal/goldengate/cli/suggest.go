package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/discover"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/project"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/rules"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

// A project suggestion is for a folder that has sessions and is in no
// project: Golden Gate says which project it probably belongs to, and why.
// Like a rule offer it is only an offer: the folder becomes a member (and so
// shared) only when the user says yes.
//
// Suggestions never read a session of the unshared folder. They use what is
// known about the folder itself: the repository root its tool recorded, and,
// only when the user turned on enrich.git, the repository's remote and
// current branch.

// offer is something Golden Gate proposes and the user answers: a rule
// (offers.go) or a project for a folder.
type offer struct {
	ID       string
	Key      string // what "not now" is remembered under
	Kind     string // "rule" or "project"
	Question string
	Because  string
	Examples []string // rule offers: a few of the sessions it would have matched
	Effect   []string // exactly what saying yes changes
	// accept applies the offer to cfg (saved by the caller) and says what
	// was done.
	accept func(cfg *config.Config) (string, error)
	// after runs once the config is saved.
	after func(ctx context.Context, st *gsync.State) error
}

// commands are the two answers on the command line.
func (o offer) commands() (yes, notNow string) {
	group := "rules"
	if o.Kind == "project" {
		group = "projects"
	}
	return "goldengate " + group + " accept " + o.ID, "goldengate " + group + " dismiss " + o.ID
}

// projectSuggestions returns a suggestion for every unshared folder that
// has one, strongest reason first within a folder.
func (a *app) projectSuggestions(ctx context.Context, cfg config.Config, st *gsync.State) ([]offer, error) {
	m, err := a.matcher(cfg)
	if err != nil {
		return nil, err
	}
	reg, err := a.registry(ctx)
	if err != nil {
		return nil, err
	}
	var since time.Time
	if d, err := config.ParseSince(cfg.Share.Since); err == nil && d > 0 {
		since = a.Now().Add(-d)
	}
	found, err := discover.Projects(ctx, reg, m, since, a.resolver(cfg))
	if err != nil {
		return nil, err
	}
	dismissed, err := st.DismissedOffers(ctx)
	if err != nil {
		return nil, err
	}
	home := pathutil.Normalize(a.Home)
	label := func(key string) string { return pathutil.Scrub(key, home) }

	// the folders each project names, and (with enrich.git) their remotes
	type member struct {
		project int
		key     string
	}
	var members []member
	byKey := map[string]int{}
	for i, p := range cfg.Projects {
		for _, mb := range p.Members {
			if strings.ContainsAny(mb.Path, "*?[{") || pathutil.IsVirtualKey(mb.Path) {
				continue
			}
			k := a.localKey(mb.Path)
			members = append(members, member{i, k})
			byKey[k] = i
		}
	}
	useGit := cfg.Enrich.Git
	remotes := map[string]member{} // normalized remote -> the member that has it
	ambiguous := map[string]bool{}
	if useGit {
		for _, mb := range members {
			r, _ := project.GitInfo(mb.key)
			if r = strings.ToLower(rules.NormalizeRemote(r)); r == "" {
				continue
			}
			if other, ok := remotes[r]; ok && other.project != mb.project {
				ambiguous[r] = true
			}
			remotes[r] = mb
		}
	}
	// the issue keys each project already has: from what was sent, and from
	// the prefixes the user gave the project
	issues := map[string]int{} // bare key ("LIN-412") -> project index, -1 when in several
	if useGit {
		ids := map[string]int{}
		for i, p := range cfg.Projects {
			ids[p.ID] = i
		}
		payloads, err := st.Payloads(ctx)
		if err != nil {
			return nil, err
		}
		for _, raw := range payloads {
			var p model.SessionPayload
			if json.Unmarshal(raw, &p) != nil {
				continue
			}
			i, ok := ids[p.ProjectID]
			if !ok {
				continue
			}
			for _, it := range p.WorkItems {
				if it.Kind != model.KindIssue || (it.Provider != "linear" && it.Provider != "jira") {
					continue
				}
				_, bare, _ := strings.Cut(it.Key, ":")
				if cur, seen := issues[bare]; seen && cur != i {
					issues[bare] = -1
				} else {
					issues[bare] = i
				}
			}
		}
	}

	var out []offer
	for _, f := range found.Projects {
		if f.Decision.Shared || m.Denied(f.Key) || f.Sessions == 0 {
			continue
		}
		if pathutil.IsVirtualKey(f.Key) {
			// a project the user made in claude.ai or ChatGPT is a named
			// project in waiting; catch-all groupings are not
			if o, ok := a.chatProjectOffer(cfg, f.Key, f.Sessions, dismissed); ok {
				out = append(out, o)
			}
			continue
		}
		if len(cfg.Projects) == 0 {
			continue
		}
		pi, reason := -1, ""
		if i, ok := byKey[f.Root]; ok && f.Root != "" {
			pi, reason = i, fmt.Sprintf("It is a worktree of %s, which is in the project.", label(f.Root))
		}
		if pi < 0 && useGit {
			remote, branch := project.GitInfo(f.Key)
			if r := strings.ToLower(rules.NormalizeRemote(remote)); r != "" && !ambiguous[r] {
				if mb, ok := remotes[r]; ok {
					pi, reason = mb.project, fmt.Sprintf("It has the same repository remote as %s (%s).", label(mb.key), rules.NormalizeRemote(remote))
				}
			}
			if pi < 0 {
				for _, k := range workitems.BranchKeys(branch, nil) {
					if i, ok := issues[k]; ok && i >= 0 {
						pi, reason = i, fmt.Sprintf("Its branch %s names %s, an issue the project already has.", branch, k)
						break
					}
					for i, p := range cfg.Projects {
						for _, prefix := range p.WorkItems {
							if pi < 0 && (rules.KeyHasPrefix(k, prefix) || rules.KeyHasPrefix("linear:"+k, prefix) || rules.KeyHasPrefix("jira:"+k, prefix)) {
								pi, reason = i, fmt.Sprintf("Its branch %s names %s, which is under the project's work-item prefix %s.", branch, k, prefix)
							}
						}
					}
					if pi >= 0 {
						break
					}
				}
			}
		}
		key := "project-for-folder\x00" + f.Key
		if pi < 0 || dismissed[key] {
			continue
		}
		p := cfg.Projects[pi]
		folder, folderKey, n := label(f.Key), f.Key, f.Sessions
		effect := []string{fmt.Sprintf("adds %s to project %q, which shares it", folder, p.Name)}
		if p.Auto() && !hasApproveRule(p) {
			effect = append(effect, fmt.Sprintf("the project is at trust level auto, so its %d session(s) go out the next time you send", n))
		} else {
			effect = append(effect, fmt.Sprintf("its %d session(s) then wait in the inbox for your approval; nothing is sent by accepting", n))
		}
		pid, pname := p.ID, p.Name
		out = append(out, offer{
			ID: "s_" + offerID(key)[2:], Key: key, Kind: "project",
			Question: fmt.Sprintf("Add %s to project %q?", folder, p.Name),
			Because:  fmt.Sprintf("%d session(s) there are in no project. %s", n, reason),
			Effect:   effect,
			accept: func(cfg *config.Config) (string, error) {
				if cfg.ProjectIndex(pid) < 0 {
					return "", fmt.Errorf("project %q no longer exists", pname)
				}
				if _, err := cfg.AddMember(pid, folder); err != nil {
					return "", err
				}
				return fmt.Sprintf("%q: added %s (shared)", pname, folder), nil
			},
			after: func(ctx context.Context, st *gsync.State) error { return st.ClearTombstone(ctx, folderKey) },
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Question < out[j].Question })
	return out, nil
}

// chatProjectOffer suggests a named project for a chat project of
// claude.ai or ChatGPT: a grouping the user created there themselves. The
// catch-all groupings (ungrouped chats, GPTs and projects known only by an
// id) get no suggestion: their conversations are shared one at a time with
// `goldengate chats`. Only the grouping's name is used, never its content.
func (a *app) chatProjectOffer(cfg config.Config, key string, sessions int, dismissed map[string]bool) (offer, bool) {
	source, name, _ := strings.Cut(key, ":")
	if (source != "claude-ai" && source != "chatgpt") || name == "" || name == "Chats" || strings.HasPrefix(name, "project-") || strings.HasPrefix(name, "gpt-") {
		return offer{}, false
	}
	if limited, _ := cfg.SessionScope(key, ""); limited {
		return offer{}, false // the user is already sharing it by conversation
	}
	okey := "project-for-chat-grouping\x00" + key
	if dismissed[okey] {
		return offer{}, false
	}
	product := map[string]string{"claude-ai": "claude.ai", "chatgpt": "ChatGPT"}[source]
	target, existing := name, cfg.ProjectIndex(name) >= 0
	effect := []string{fmt.Sprintf("creates the project %q with %s as its member, which shares all %d conversation(s) of it", target, key, sessions)}
	if existing {
		effect[0] = fmt.Sprintf("adds %s to your project %q, which shares all %d conversation(s) of it", key, target, sessions)
	}
	if i := cfg.ProjectIndex(name); i >= 0 && cfg.Projects[i].Auto() && !hasApproveRule(cfg.Projects[i]) {
		effect = append(effect, "the project is at trust level auto, so they go out the next time you send")
	} else {
		effect = append(effect, "they then wait in the inbox for your approval; nothing is sent by accepting")
	}
	effect = append(effect, "to share only some of them instead: goldengate chats --all")
	return offer{
		ID: "s_" + offerID(okey)[2:], Key: okey, Kind: "project",
		Question: fmt.Sprintf("Make %q a project?", name),
		Because:  fmt.Sprintf("It is a project in %s with %d conversation(s), and is in no Golden Gate project.", product, sessions),
		Effect:   effect,
		accept: func(cfg *config.Config) (string, error) {
			if _, err := cfg.AddMember(target, key); err != nil {
				return "", err
			}
			return fmt.Sprintf("%q: added %s (shared)", target, key), nil
		},
		after: func(ctx context.Context, st *gsync.State) error { return st.ClearTombstone(ctx, key) },
	}, true
}

// allOffers is every offer that is due: rules first, then projects.
func (a *app) allOffers(ctx context.Context, cfg config.Config, st *gsync.State) ([]offer, error) {
	ro, err := a.ruleOffers(ctx, cfg, st)
	if err != nil {
		return nil, err
	}
	out := make([]offer, 0, len(ro))
	for _, o := range ro {
		out = append(out, o.offer(a))
	}
	ps, err := a.projectSuggestions(ctx, cfg, st)
	if err != nil {
		return nil, err
	}
	return append(out, ps...), nil
}

func (a *app) projectOfferCmds() []*cobra.Command {
	suggest := &cobra.Command{Use: "suggest", Short: "Folders with sessions that are in no project, and the project each probably belongs to", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withOffers(c.Context(), func(cfg config.Config, _ *gsync.State, offers []offer) error {
				n := 0
				for _, o := range offers {
					if o.Kind == "project" {
						n++
					}
				}
				if n == 0 {
					fmt.Fprintln(a.Stdout, "no suggestions: no unshared folder with sessions looks like it belongs to one of your projects")
					if !cfg.Enrich.Git {
						fmt.Fprintln(a.Stdout, "Only worktrees recorded by your tools are recognized. To also compare repository remotes and branch names,\nset `git = true` under [enrich] in the config: Golden Gate then reads each folder's git HEAD and config files (never its content).")
					}
					return nil
				}
				fmt.Fprintln(a.Stdout, "Suggestions only: nothing is shared unless you accept.")
				a.printOffers(offers, "project")
				return nil
			})
		}}
	accept := &cobra.Command{Use: "accept <suggestion-id>", Short: "Accept a suggestion: add the folder to the suggested project", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.answerOffer(c.Context(), args[0], true) }}
	dismiss := &cobra.Command{Use: "dismiss <suggestion-id>", Short: "Say not now to a suggestion; it is not made again", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return a.answerOffer(c.Context(), args[0], false) }}
	return []*cobra.Command{suggest, accept, dismiss}
}

func (a *app) answerOffer(ctx context.Context, id string, accept bool) error {
	msg, err := a.decideOffer(ctx, id, accept)
	if err == nil {
		fmt.Fprintln(a.Stdout, msg)
	}
	return err
}
