package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/goldengate/internal/client"
	"github.com/OWNER/goldengate/internal/config"
	"github.com/OWNER/goldengate/internal/pathutil"
	"github.com/OWNER/goldengate/internal/secrets"
)

const (
	envServer = "GOLDENGATE_SERVER_URL"
	envAdmin  = "GOLDENGATE_ADMIN_TOKEN"
)

func (a *app) userAgent() string { return "goldengate/" + Version }

func (a *app) secretStore() secrets.Store {
	if a.SecretStore == nil {
		return &secrets.Memory{}
	}
	return a.SecretStore(a.dirs().State, func(msg string) { fmt.Fprintln(a.Stderr, "warning:", msg) })
}

// secretName keys the ingest token by server host, so two servers can coexist.
func secretName(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return "ingest-token"
	}
	return "ingest-token@" + u.Host
}

// ingestClient builds a client for the configured server using the stored ingest token.
func (a *app) ingestClient(cfg config.Config) (*client.Client, error) {
	if cfg.Server.URL == "" {
		return nil, errors.New("no server configured: run `goldengate init` first")
	}
	tok, err := a.secretStore().Get(secretName(cfg.Server.URL))
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, errors.New("no ingest token stored for this server: run `goldengate init` again")
	}
	if err != nil {
		return nil, err
	}
	return client.New(cfg.Server.URL, tok, a.HTTP, a.userAgent())
}

func (a *app) prompt(q string) (string, error) {
	fmt.Fprint(a.Stderr, q)
	line, err := bufio.NewReader(a.stdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (a *app) stdin() io.Reader {
	if a.Stdin == nil {
		return strings.NewReader("")
	}
	return a.Stdin
}

// adminToken finds the admin token without ever writing it anywhere:
// environment, then stdin (when asked), then a hidden prompt.
func (a *app) adminToken(fromStdin bool, nonInteractive bool) (string, error) {
	if v := strings.TrimSpace(a.Getenv(envAdmin)); v != "" {
		return v, nil
	}
	if fromStdin {
		b, err := io.ReadAll(io.LimitReader(a.stdin(), 4096))
		if err != nil {
			return "", fmt.Errorf("reading admin token from stdin: %w", err)
		}
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, nil
		}
		return "", errors.New("--admin-token-stdin given but stdin was empty")
	}
	if a.Interactive && !nonInteractive && a.ReadSecret != nil {
		v, err := a.ReadSecret("Admin token (input hidden): ")
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("admin token required: set %s, or pass --admin-token-stdin, or run interactively", envAdmin)
}

// --- init ---

func (a *app) initCmd() *cobra.Command {
	var serverURL, pairCode string
	var stdinTok, nonInteractive bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Connect to your goldengatebridge server and create scoped tokens",
		Long: `Connects this machine to a deployed goldengatebridge.

With the admin secret (GOLDENGATE_ADMIN_TOKEN, --admin-token-stdin, or a hidden prompt) it
creates an ingest token (kept in your OS keyring) and a read token (shown once, for
your assistant). With --pair-code it exchanges a single-use code from
'goldengatebridge pair' for the ingest token instead. The admin secret is kept in
memory only and is never written to disk.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runInit(cmd.Context(), serverURL, pairCode, stdinTok, nonInteractive)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "server URL (env "+envServer+")")
	cmd.Flags().StringVar(&pairCode, "pair-code", "", "single-use pairing code from `goldengatebridge pair`")
	cmd.Flags().BoolVar(&stdinTok, "admin-token-stdin", false, "read the admin token from stdin")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never prompt (use flags and environment only)")
	return cmd
}

// connectResult is what connecting to a server produced.
type connectResult struct {
	ServerURL  string
	ReadSecret string // shown once; empty when pairing was used
	OAuth      bool
	StoreKind  string
}

// connect checks the server, obtains an ingest token (and, with the admin
// token, a read token), stores the ingest token and records the server URL.
// The admin token is used for these calls only and is never stored.
func (a *app) connect(ctx context.Context, serverURL, pairCode, adminToken string) (connectResult, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if err := config.CheckServerURL(serverURL); err != nil { // refuses plain http except loopback
		return connectResult{}, err
	}
	cfg, err := a.load()
	if err != nil {
		return connectResult{}, err
	}
	probe, err := client.New(serverURL, "", a.HTTP, a.userAgent())
	if err != nil {
		return connectResult{}, err
	}
	if err := probe.Health(ctx); err != nil {
		return connectResult{}, fmt.Errorf("server is not reachable at %s: %w", serverURL, err)
	}
	store := a.secretStore()
	res := connectResult{ServerURL: serverURL, StoreKind: store.Kind()}
	var ingestSecret string
	if pairCode != "" {
		if ingestSecret, err = client.Pair(ctx, serverURL, pairCode, a.HTTP, a.userAgent()); err != nil {
			return connectResult{}, fmt.Errorf("pairing failed: %w", err)
		}
	} else {
		ac, err := client.New(serverURL, adminToken, a.HTTP, a.userAgent())
		if err != nil {
			return connectResult{}, err
		}
		host := "unknown"
		if a.Hostname != nil {
			host = a.Hostname()
		}
		in, err := ac.MintToken(ctx, "ingest", "goldengate@"+host, "")
		if err != nil {
			return connectResult{}, fmt.Errorf("could not create the ingest token (is the admin token right?): %w", err)
		}
		rd, err := ac.MintToken(ctx, "read", "assistant", "")
		if err != nil {
			return connectResult{}, fmt.Errorf("could not create the read token: %w", err)
		}
		ingestSecret, res.ReadSecret = in.Secret, rd.Secret
	}
	if err := store.Set(secretName(serverURL), ingestSecret); err != nil {
		return connectResult{}, fmt.Errorf("could not store the ingest token: %w", err)
	}
	cfg.Server.URL = serverURL
	if err := config.Save(a.cfgPath(), cfg); err != nil {
		return connectResult{}, err
	}
	res.OAuth = probe.OAuthEnabled(ctx)
	return res, nil
}

func (a *app) runInit(ctx context.Context, serverURL, pairCode string, stdinTok, nonInteractive bool) error {
	var err error
	if serverURL == "" {
		serverURL = strings.TrimSpace(a.Getenv(envServer))
	}
	if serverURL == "" && a.Interactive && !nonInteractive {
		if serverURL, err = a.prompt("Server URL (https://...): "); err != nil {
			return err
		}
	}
	if serverURL == "" {
		return fmt.Errorf("server URL required: pass --server or set %s", envServer)
	}
	admin := ""
	if pairCode == "" {
		if admin, err = a.adminToken(stdinTok, nonInteractive); err != nil {
			return err
		}
	}
	res, err := a.connect(ctx, serverURL, pairCode, admin)
	if err != nil {
		return err
	}
	serverURL, readSecret, oauth := res.ServerURL, res.ReadSecret, res.OAuth
	fmt.Fprintf(a.Stdout, "Server %s is up.\n", serverURL)
	fmt.Fprintf(a.Stdout, "Ingest token stored in the %s. The admin token was not saved anywhere.\n", res.StoreKind)
	if readSecret != "" {
		fmt.Fprintf(a.Stdout, `
Your assistant's read token (shown ONCE; it cannot be recovered, rotate it with 'goldengate token rotate read'):

    %s

Connector URL: %s/mcp
For assistants that let you set a header, paste the URL and send the token as "Authorization: Bearer <token>".
`, readSecret, serverURL)
	} else {
		fmt.Fprintf(a.Stdout, "\nCreate the assistant's read token on the server with:\n    goldengatebridge token create --scope read\n")
	}
	if oauth {
		fmt.Fprintf(a.Stdout, `
This server has OAuth enabled, so assistants that connect with OAuth need no token:
add %s/mcp as the connector URL (Gemini: Settings > Connected Apps > Add a custom app).
A consent page opens in your browser; approve it with your admin secret. If the assistant asks
for a client ID and secret instead, create them with 'goldengatebridge oauth clients create'.
`, serverURL)
	} else {
		fmt.Fprint(a.Stdout, "\nAssistants that only support OAuth (Gemini's custom apps are one) need the server started with --oauth.\n")
	}
	fmt.Fprint(a.Stdout, `
Next:
    goldengate share add ~/path/to/project
    goldengate sync --dry-run     # see exactly what would be sent
    goldengate sync
`)
	return nil
}

// --- token rotate ---

func (a *app) tokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage server tokens"}
	var grace time.Duration
	var stdinTok bool
	rotate := &cobra.Command{
		Use:   "rotate read|ingest",
		Short: "Mint a replacement token and revoke the old ones (needs the admin token)",
		Long: `Mints a new token of the given scope, then revokes every other active token of that
scope, optionally after --grace so a connector can be updated first. A new read token is
shown once; a new ingest token replaces the stored one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.rotate(cmd.Context(), args[0], grace, stdinTok)
		},
	}
	rotate.Flags().DurationVar(&grace, "grace", 0, "keep the old token(s) valid for this long")
	rotate.Flags().BoolVar(&stdinTok, "admin-token-stdin", false, "read the admin token from stdin")
	cmd.AddCommand(rotate)
	return cmd
}

func (a *app) rotate(ctx context.Context, scope string, grace time.Duration, stdinTok bool) error {
	if scope != "read" && scope != "ingest" {
		return fmt.Errorf("scope must be read or ingest, got %q", scope)
	}
	cfg, err := a.load()
	if err != nil {
		return err
	}
	if cfg.Server.URL == "" {
		return errors.New("no server configured: run `goldengate init` first")
	}
	admin, err := a.adminToken(stdinTok, false)
	if err != nil {
		return err
	}
	ac, err := client.New(cfg.Server.URL, admin, a.HTTP, a.userAgent())
	if err != nil {
		return err
	}
	old, err := ac.ListTokens(ctx)
	if err != nil {
		return fmt.Errorf("could not list tokens: %w", err)
	}
	host := "unknown"
	if a.Hostname != nil {
		host = a.Hostname()
	}
	label := "assistant"
	if scope == "ingest" {
		label = "goldengate@" + host
	}
	fresh, err := ac.MintToken(ctx, scope, label, "")
	if err != nil {
		return fmt.Errorf("could not mint the replacement: %w", err)
	}
	if scope == "ingest" {
		if err := a.secretStore().Set(secretName(cfg.Server.URL), fresh.Secret); err != nil {
			return fmt.Errorf("could not store the new ingest token (old tokens were left active): %w", err)
		}
	}
	revoked := 0
	for _, t := range old {
		if t.Scope != scope || !t.Active || t.ID == fresh.ID {
			continue
		}
		if err := ac.RevokeToken(ctx, t.ID, grace); err != nil {
			fmt.Fprintf(a.Stderr, "could not revoke %s: %v\n", t.ID, err)
			continue
		}
		revoked++
	}
	when := "immediately"
	if grace > 0 {
		when = "after " + grace.String()
	}
	fmt.Fprintf(a.Stdout, "Created a new %s token and revoked %d old one(s) %s.\n", scope, revoked, when)
	if scope == "read" {
		fmt.Fprintf(a.Stdout, "\nNew read token (shown ONCE):\n\n    %s\n\nUpdate your assistant's connector (%s/mcp) with it.\n", fresh.Secret, cfg.Server.URL)
	}
	return nil
}

// --- purge ---

func (a *app) purgeCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "purge --project PATH",
		Short: "Delete one project's data from the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" {
				return errors.New("--project is required")
			}
			cfg, err := a.load()
			if err != nil {
				return err
			}
			cl, err := a.ingestClient(cfg)
			if err != nil {
				return err
			}
			key := project
			if !pathutil.IsVirtualKey(project) {
				key = pathutil.Scrub(pathutil.Normalize(pathutil.ExpandHome(project, a.Home)), pathutil.Normalize(a.Home))
			}
			n, err := cl.DeleteProject(cmd.Context(), key)
			if err != nil {
				return err
			}
			st, err := a.openState()
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			if err := st.ForgetProject(cmd.Context(), a.localKey(project)); err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "deleted %d session(s) of %s from the server.\n", n, key)
			if m, err := a.matcher(cfg); err == nil && m.Decide(a.localKey(project), "").Shared {
				fmt.Fprintln(a.Stdout, "Note: this project is still shared, so the next sync uploads it again. Use `goldengate share remove` to stop sharing it.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project folder to delete from the server")
	return cmd
}

// --- doctor (server part) ---

func (a *app) doctorServer(ctx context.Context, cfg config.Config) int {
	w := a.Stdout
	probe, err := client.New(cfg.Server.URL, "", a.HTTP, a.userAgent())
	if err != nil {
		fmt.Fprintf(w, "server  FAIL  %v\n", err)
		return 1
	}
	if err := probe.Health(ctx); err != nil {
		fmt.Fprintf(w, "server  FAIL  %s unreachable: %v\n", cfg.Server.URL, err)
		return 1
	}
	cl, err := a.ingestClient(cfg)
	if err != nil {
		fmt.Fprintf(w, "server  FAIL  %s is up, but: %v\n", cfg.Server.URL, err)
		return 1
	}
	ps, err := cl.ListProjects(ctx)
	if err != nil {
		fmt.Fprintf(w, "server  FAIL  %s is up, but the stored ingest token was refused: %v\n", cfg.Server.URL, err)
		return 1
	}
	fmt.Fprintf(w, "server  ok    %s (token ok, %d project(s) stored)\n", cfg.Server.URL, len(ps))
	return 0
}

func newProbe(a *app, serverURL string) (*client.Client, error) {
	return client.New(serverURL, "", a.HTTP, a.userAgent())
}
