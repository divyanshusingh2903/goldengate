// Package bridgecli builds the goldengatebridge command tree.
package bridgecli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/auth"
	"github.com/divyanshusingh2903/goldengate/internal/goldengatebridge/server/store"
)

// Version is set at build time.
var Version = "dev"

// Deps are the command tree's external dependencies.
type Deps struct {
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Now            func() time.Time
}

// DefaultDeps wires the real process environment.
func DefaultDeps() Deps {
	return Deps{Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv, Now: time.Now}
}

type app struct {
	Deps
	dbPath string
}

// New returns the root command.
func New(d Deps) *cobra.Command {
	a := &app{Deps: d}
	root := &cobra.Command{Use: "goldengatebridge", Short: "Golden Gate server: stores shared work summaries and serves them over MCP",
		SilenceUsage: true, SilenceErrors: true}
	root.SetOut(d.Stdout)
	root.SetErr(d.Stderr)
	root.PersistentFlags().StringVar(&a.dbPath, "db", "", "SQLite database path (default $GOLDENGATE_DB or ./goldengatebridge.db)")
	root.AddCommand(a.serveCmd(), a.pairCmd(), a.tokenCmd(), a.oauthCmd(), &cobra.Command{
		Use: "version", Short: "Print the version",
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(a.Stdout, "goldengatebridge", Version, runtime.GOOS+"/"+runtime.GOARCH)
		},
	})
	return root
}

func (a *app) db() string {
	switch {
	case a.dbPath != "":
		return a.dbPath
	case a.Getenv("GOLDENGATE_DB") != "":
		return a.Getenv("GOLDENGATE_DB")
	}
	return "goldengatebridge.db"
}

func (a *app) open(ctx context.Context) (store.Store, error) { return store.OpenSQLite(ctx, a.db()) }

// --- serve ---

func (a *app) serveCmd() *cobra.Command {
	var addr, cert, key, publicURL string
	var behindProxy, enableOAuth bool
	var redirectHosts, origins []string
	var hops, perProject, total int
	var retention time.Duration
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the server (ingest API + MCP endpoint)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("addr") && a.Getenv("PORT") != "" { // Cloud Run and similar
				addr = ":" + a.Getenv("PORT")
			}
			if enableOAuth && publicURL == "" {
				publicURL = a.Getenv("GOLDENGATE_PUBLIC_URL")
			}
			if enableOAuth && publicURL == "" {
				return errors.New("--oauth needs --public-url (or GOLDENGATE_PUBLIC_URL): the externally visible https URL of this server")
			}
			if !enableOAuth {
				publicURL, redirectHosts = "", nil
			}
			return a.serve(cmd.Context(), serveOpts{addr: addr, cert: cert, key: key, behindProxy: behindProxy, hops: hops,
				publicURL: publicURL, redirectHosts: redirectHosts, origins: origins,
				limits: store.Limits{MaxSessionsPerProject: perProject, MaxSessionsTotal: total}, retention: retention})
		},
	}
	f := cmd.Flags()
	f.StringVar(&addr, "addr", "127.0.0.1:8080", "listen address (env PORT overrides the port when --addr is not set)")
	f.StringVar(&cert, "tls-cert", "", "TLS certificate file")
	f.StringVar(&key, "tls-key", "", "TLS key file")
	f.BoolVar(&behindProxy, "behind-tls-proxy", false, "a proxy terminates TLS in front of this server (trusts X-Forwarded-For)")
	f.IntVar(&hops, "proxy-hops", 1, "number of proxies in front, for X-Forwarded-For")
	f.IntVar(&perProject, "max-sessions-per-project", 5000, "storage quota per project")
	f.IntVar(&total, "max-sessions-total", 100000, "storage quota overall")
	f.BoolVar(&enableOAuth, "oauth", false, "enable the built-in OAuth 2.1 server so OAuth-only assistants (e.g. Gemini) can connect")
	f.StringVar(&publicURL, "public-url", "", "with --oauth: the externally visible https URL of this server (env GOLDENGATE_PUBLIC_URL)")
	f.StringSliceVar(&redirectHosts, "oauth-redirect-host", nil, "with --oauth: only allow https redirect URIs on these hosts (repeatable)")
	f.StringSliceVar(&origins, "allowed-origin", nil, "browser origin (scheme://host[:port]) allowed to call /mcp; requests with any other Origin get 403 (repeatable)")
	f.DurationVar(&retention, "audit-retention", 90*24*time.Hour, "how long to keep the audit log")
	return cmd
}

type serveOpts struct {
	addr, cert, key string
	behindProxy     bool
	hops            int
	limits          store.Limits
	retention       time.Duration
	publicURL       string
	redirectHosts   []string
	origins         []string
}

func (a *app) serve(ctx context.Context, o serveOpts) error {
	if (o.cert == "") != (o.key == "") {
		return errors.New("--tls-cert and --tls-key must be given together")
	}
	if err := server.CheckListen(o.addr, o.cert != "", o.behindProxy); err != nil {
		return err
	}
	st, err := a.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// Weak or malformed admin secrets stop startup; so does having no credentials at all.
	au, err := auth.New(st, auth.Options{AdminSecret: a.Getenv("GOLDENGATE_ADMIN_TOKEN"), Now: a.Now})
	if err != nil {
		return err
	}
	active, err := st.CountActiveTokens(ctx, a.Now())
	if err != nil {
		return err
	}
	if !au.HasAdmin() && active == 0 {
		return errors.New("refusing to start without credentials: set GOLDENGATE_ADMIN_TOKEN (generate with `openssl rand -base64 32`) or create a token with `goldengatebridge token create`")
	}

	log := slog.New(slog.NewJSONHandler(a.Stderr, nil))
	srv, err := server.New(server.Config{Store: st, Auth: au, Log: log, Now: a.Now, Version: Version, Limits: o.limits,
		BehindProxy: o.behindProxy, ProxyHops: o.hops, AuditRetention: o.retention,
		PublicURL: o.publicURL, OAuthRedirectHosts: o.redirectHosts, AllowedOrigins: o.origins})
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	hs := &http.Server{
		Handler: srv, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	mctx, stopMaint := context.WithCancel(ctx)
	defer stopMaint()
	go srv.Maintain(mctx)

	errc := make(chan error, 1)
	go func() {
		if o.cert != "" {
			errc <- hs.ServeTLS(ln, o.cert, o.key)
		} else {
			errc <- hs.Serve(ln)
		}
	}()
	fmt.Fprintf(a.Stderr, "listening on %s (tls=%v, admin=%v, tokens=%d, oauth=%v)\n", ln.Addr(), o.cert != "", au.HasAdmin(), active, o.publicURL != "")
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	}
}

// --- pair ---

func (a *app) pairCmd() *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{
		Use: "pair", Short: "Print a single-use pairing code for `goldengate init --pair-code`", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			au, err := auth.New(st, auth.Options{Now: a.Now})
			if err != nil {
				return err
			}
			code, exp, err := au.CreatePairing(cmd.Context(), ttl)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "Pairing code: %s\nValid until %s, usable once. Run on your laptop:\n  goldengate init --server https://YOUR-HOST --pair-code %s\n",
				code, exp.Local().Format("15:04:05"), code)
			return nil
		},
	}
	cmd.Flags().DurationVar(&ttl, "ttl", 10*time.Minute, "how long the code stays valid")
	return cmd
}

// --- token ---

func (a *app) tokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage tokens directly against the database (local admin)"}
	var scope, label string
	var expires time.Duration
	create := &cobra.Command{
		Use: "create", Short: "Mint a token (shown once)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			au, err := auth.New(st, auth.Options{Now: a.Now})
			if err != nil {
				return err
			}
			secret, tok, err := au.Mint(cmd.Context(), scope, label, expires)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "%s\n", secret)
			fmt.Fprintf(a.Stderr, "Created %s token %s. This is the only time it is shown; it cannot be recovered.\n", tok.Scope, tok.ID)
			return nil
		},
	}
	create.Flags().StringVar(&scope, "scope", "", "ingest or read")
	create.Flags().StringVar(&label, "label", "", "a note about who or what uses this token")
	create.Flags().DurationVar(&expires, "expires", 0, "expire after this duration (default: never)")
	_ = create.MarkFlagRequired("scope")

	list := &cobra.Command{
		Use: "list", Short: "List tokens (never shows secrets)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			toks, err := st.ListTokens(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSCOPE\tLABEL\tCREATED\tEXPIRES\tLAST USED\tSTATE")
			now := a.Now()
			for _, t := range toks {
				state := "active"
				if !t.Active(now) {
					state = "inactive"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Scope, t.Label, day(t.CreatedAt), day(t.ExpiresAt), day(t.LastUsedAt), state)
			}
			return w.Flush()
		},
	}

	var grace time.Duration
	revoke := &cobra.Command{
		Use: "revoke <id>", Short: "Revoke a token (immediately, or after --grace)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			au, err := auth.New(st, auth.Options{Now: a.Now})
			if err != nil {
				return err
			}
			if err := au.Revoke(cmd.Context(), args[0], grace); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("no token with id %s", args[0])
				}
				return err
			}
			fmt.Fprintf(a.Stdout, "revoked %s\n", args[0])
			return nil
		},
	}
	revoke.Flags().DurationVar(&grace, "grace", 0, "keep the token valid for this long (rotation)")
	cmd.AddCommand(create, list, revoke)
	return cmd
}

func day(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return strings.TrimSuffix(t.UTC().Format("2006-01-02 15:04"), " 00:00")
}

// --- oauth clients ---

func (a *app) oauthCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "oauth", Short: "Manage OAuth clients (assistants that connected with OAuth)"}
	clients := &cobra.Command{Use: "clients", Short: "List, create and delete OAuth clients"}

	list := &cobra.Command{
		Use: "list", Short: "List registered clients", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			cs, err := st.ListOAuthClients(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tREDIRECT\tTYPE\tCREATED\tLAST USED")
			for _, c := range cs {
				kind := "public"
				if c.SecretHash != "" {
					kind = "confidential"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, strings.Join(c.RedirectURIs, ","), kind, day(c.CreatedAt), day(c.LastUsedAt))
			}
			return w.Flush()
		},
	}

	var name string
	var redirects []string
	var public bool
	create := &cobra.Command{
		Use: "create", Short: "Register a client by hand (for assistants that ask you to type a client ID and secret)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(redirects) == 0 {
				return errors.New("at least one --redirect-uri is required (your assistant shows the address to register)")
			}
			for _, u := range redirects {
				if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") && !strings.HasPrefix(u, "http://127.0.0.1") {
					return fmt.Errorf("redirect URI %q must be https (or http on loopback)", u)
				}
			}
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			c := store.OAuthClient{ID: auth.RandomSecret("gg_", 18), Name: name, RedirectURIs: redirects, CreatedAt: a.Now().UTC()}
			secret := ""
			if !public {
				secret = auth.RandomSecret("ggs_", 32)
				c.SecretHash = auth.HashToken(secret)
			}
			if err := st.CreateOAuthClient(cmd.Context(), c); err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "client_id:     %s\n", c.ID)
			if secret != "" {
				fmt.Fprintf(a.Stdout, "client_secret: %s\n", secret)
				fmt.Fprintln(a.Stderr, "The client secret is shown only now and cannot be recovered.")
			}
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "manual client", "name shown on the consent page")
	create.Flags().StringSliceVar(&redirects, "redirect-uri", nil, "allowed redirect URI (repeatable)")
	create.Flags().BoolVar(&public, "public", false, "public client (PKCE only, no client secret)")

	del := &cobra.Command{
		Use: "delete <client-id>", Short: "Delete a client and revoke everything it was granted", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			n, err := st.DeleteOAuthClient(cmd.Context(), args[0], a.Now())
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no client with id %s", args[0])
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "deleted client %s and revoked %d access token(s)\n", args[0], n)
			return nil
		},
	}
	clients.AddCommand(list, create, del)
	cmd.AddCommand(clients)
	return cmd
}
