package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/rules"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/pathutil"
)

// A rule offer is Golden Gate noticing that the user keeps making the same
// decision by hand and asking whether a rule should make it. It is only ever
// an offer: nothing changes until the user accepts, and "not now" is
// remembered. Offers are computed from what was sent and why (facts in the
// local state), never from what a model wrote.

const (
	// offerThreshold is how many sessions from one folder or chat grouping
	// the user must have sent by hand, within offerWindow, before an offer.
	offerThreshold = 5
	offerWindow    = 30 * 24 * time.Hour
)

// ruleOffer proposes one rule for one project.
type ruleOffer struct {
	ID          string // short, for the command line
	Key         string // what "not now" is remembered under
	ProjectID   string
	ProjectName string
	Rule        rules.Rule // without an id until accepted
	// SetTrust is the trust level accepting also sets, or "" when the
	// project is already at auto.
	SetTrust string
	// Count is how many sessions the rule would have matched in the window:
	// the ones the user sent by hand. Examples are a few of their titles.
	Count    int
	Examples []string
}

// Question is the offer in one sentence.
func (o ruleOffer) Question() string {
	return fmt.Sprintf("Share sessions from %s automatically?", o.Rule.Folder)
}

// Effect says exactly what accepting changes.
func (o ruleOffer) Effect() []string {
	out := []string{fmt.Sprintf("adds to project %q the rule: %s", o.ProjectName, o.Rule)}
	if o.SetTrust != "" {
		out = append(out, fmt.Sprintf("sets the project's trust level to %s, so sessions the rule matches are sent on the next sync; everything else in the project still waits in the inbox", o.SetTrust))
	}
	return out
}

func offerID(key string) string {
	h := sha256.Sum256([]byte(key))
	return "o_" + hex.EncodeToString(h[:4])
}

// ruleOffers looks at what was sent by hand in the last 30 days and returns
// the offers that are due, most sessions first.
func (a *app) ruleOffers(ctx context.Context, cfg config.Config, st *gsync.State) ([]ruleOffer, error) {
	sent, err := st.Sent(ctx)
	if err != nil {
		return nil, err
	}
	byRules, err := st.SentByRules(ctx)
	if err != nil {
		return nil, err
	}
	dismissed, err := st.DismissedOffers(ctx)
	if err != nil {
		return nil, err
	}
	type group struct {
		project, folder string
		titles          []string
	}
	groups := map[string]*group{}
	since := a.Now().Add(-offerWindow)
	for _, e := range sent {
		if e.SyncedAt.Before(since) || byRules[e.Source+"\x00"+e.SessionID] {
			continue
		}
		var p model.SessionPayload
		if json.Unmarshal(e.Payload, &p) != nil || p.ProjectID == "" {
			continue
		}
		k := p.ProjectID + "\x00" + e.Project
		g := groups[k]
		if g == nil {
			g = &group{project: p.ProjectID, folder: e.Project}
			groups[k] = g
		}
		g.titles = append(g.titles, payloadTitle(&p))
	}
	var out []ruleOffer
	for _, g := range groups {
		if len(g.titles) < offerThreshold {
			continue
		}
		i := cfg.ProjectIndex(g.project)
		if i < 0 {
			continue
		}
		p := cfg.Projects[i]
		folder := g.folder
		if !pathutil.IsVirtualKey(folder) {
			folder = pathutil.Scrub(folder, pathutil.Normalize(a.Home))
		}
		rule := rules.Rule{Action: rules.ActionApprove, Folder: folder}
		// nothing to offer where automation already covers the folder, or
		// where the user wrote the rule and chose not to let it act
		facts := rules.Facts{Folder: g.folder}
		covered := p.Auto() && !hasApproveRule(p)
		for _, r := range p.Rules {
			if r.Action == rules.ActionApprove && r.Source == "" && r.Remote == "" && r.Branch == "" && r.WorkItem == "" && r.Match(facts, a.Home) {
				covered = true
			}
		}
		key := "approve-folder\x00" + p.ID + "\x00" + g.folder
		if covered || dismissed[key] {
			continue
		}
		o := ruleOffer{ID: offerID(key), Key: key, ProjectID: p.ID, ProjectName: p.Name, Rule: rule, Count: len(g.titles), Examples: g.titles[:min(len(g.titles), 3)]}
		if !p.Auto() {
			o.SetTrust = rules.TrustAuto
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// acceptOffer writes the offered rule (and trust level) into the config.
func (a *app) acceptOffer(cfg config.Config, o ruleOffer) (rules.Rule, error) {
	i := cfg.ProjectIndex(o.ProjectID)
	if i < 0 {
		return rules.Rule{}, fmt.Errorf("project %q no longer exists", o.ProjectName)
	}
	r := o.Rule
	r.ID = rules.NewID()
	if err := r.Validate(); err != nil {
		return rules.Rule{}, err
	}
	// a project that was automatic as a whole keeps its other sessions
	// waiting once it has an approve rule; say so through trustWords
	cfg.Projects[i].Rules = append(cfg.Projects[i].Rules, r)
	if o.SetTrust != "" {
		cfg.Projects[i].Trust = o.SetTrust
	}
	return r, config.Save(a.cfgPath(), cfg)
}

func (a *app) printOffers(offers []ruleOffer) {
	for _, o := range offers {
		fmt.Fprintf(a.Stdout, "\noffer %s: %s\n", o.ID, o.Question())
		fmt.Fprintf(a.Stdout, "  You sent %d sessions from there by hand in the last 30 days, such as:\n", o.Count)
		for _, t := range o.Examples {
			fmt.Fprintf(a.Stdout, "    %s\n", t)
		}
		fmt.Fprintln(a.Stdout, "  Saying yes:")
		for _, e := range o.Effect() {
			fmt.Fprintf(a.Stdout, "    %s\n", e)
		}
		fmt.Fprintf(a.Stdout, "  yes:      goldengate rules accept %s\n  not now:  goldengate rules dismiss %s\n", o.ID, o.ID)
	}
}

func (a *app) withOffers(ctx context.Context, fn func(cfg config.Config, st *gsync.State, offers []ruleOffer) error) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	st, err := a.openState()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	offers, err := a.ruleOffers(ctx, cfg, st)
	if err != nil {
		return err
	}
	return fn(cfg, st, offers)
}

// decideOffer accepts or dismisses the offer with the given id and says what
// happened.
func (a *app) decideOffer(ctx context.Context, id string, accept bool) (string, error) {
	var msg string
	err := a.withOffers(ctx, func(cfg config.Config, st *gsync.State, offers []ruleOffer) error {
		for _, o := range offers {
			if o.ID != id {
				continue
			}
			if !accept {
				msg = fmt.Sprintf("not now: the offer for %s will not be made again", o.Rule.Folder)
				return st.DismissOffer(ctx, o.Key, a.Now())
			}
			r, err := a.acceptOffer(cfg, o)
			if err != nil {
				return err
			}
			msg = fmt.Sprintf("%q: added rule %s: %s", o.ProjectName, r.ID, r)
			if o.SetTrust != "" {
				msg += "; trust level is now " + o.SetTrust
			}
			return nil
		}
		return fmt.Errorf("no offer %s (see `goldengate rules offers`)", id)
	})
	return msg, err
}

func (a *app) offerCmds() []*cobra.Command {
	offers := &cobra.Command{Use: "offers", Short: "Rules Golden Gate offers because you keep making the same choice by hand", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.withOffers(c.Context(), func(_ config.Config, _ *gsync.State, offers []ruleOffer) error {
				if len(offers) == 0 {
					fmt.Fprintf(a.Stdout, "no offers: one appears after you send %d sessions from the same folder or chat by hand within 30 days\n", offerThreshold)
					return nil
				}
				fmt.Fprintln(a.Stdout, strings.TrimSpace("Offers only: nothing changes unless you accept."))
				a.printOffers(offers)
				return nil
			})
		}}
	accept := &cobra.Command{Use: "accept <offer-id>", Short: "Accept an offer: add its rule (and trust level) to the project", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			msg, err := a.decideOffer(c.Context(), args[0], true)
			if err == nil {
				fmt.Fprintln(a.Stdout, msg+"\nundo with `goldengate rules remove` and `goldengate projects trust`")
			}
			return err
		}}
	dismiss := &cobra.Command{Use: "dismiss <offer-id>", Short: "Say not now to an offer; it is not made again", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			msg, err := a.decideOffer(c.Context(), args[0], false)
			if err == nil {
				fmt.Fprintln(a.Stdout, msg)
			}
			return err
		}}
	return []*cobra.Command{offers, accept, dismiss}
}
