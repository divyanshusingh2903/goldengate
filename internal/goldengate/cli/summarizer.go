package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/config"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/llm"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/secrets"
)

// summarizerSecret names the stored API key of a summarizer provider.
func summarizerSecret(provider string) string { return "summarizer-key:" + provider }

// summarizerKey finds a provider's API key: the keyring first, then the
// environment. "" with source "none" is fine for local endpoints, and for
// Anthropic lets the SDK use an `ant auth login` profile.
func (a *app) summarizerKey(provider string) (key, source string) {
	if k, err := a.secretStore().Get(summarizerSecret(provider)); err == nil && k != "" {
		return k, a.secretStore().Kind()
	}
	if p, ok := llm.Lookup(provider); ok && p.EnvKey != "" && a.Getenv(p.EnvKey) != "" {
		return a.Getenv(p.EnvKey), "$" + p.EnvKey
	}
	return "", "none"
}

// summarizer builds the configured model client, or nil for the heuristic.
func (a *app) summarizer(cfg config.Config) llm.Client {
	sc := cfg.Summarize
	if !sc.UsesModel() {
		return nil
	}
	key, _ := a.summarizerKey(sc.Provider)
	c, err := llm.New(sc.Provider, sc.BaseURL, key, sc.Model)
	if err != nil {
		return nil // config validation already rejects unknown providers
	}
	return c
}

// testSummarizer sends one tiny sample (not the user's data) to the
// configured model and describes the result.
func (a *app) testSummarizer(ctx context.Context, cfg config.Config) (string, error) {
	cl := a.summarizer(cfg)
	if cl == nil {
		return "", errors.New("the summarizer is the local heuristic: nothing to test")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	start := time.Now()
	out, err := cl.JSON(ctx, "Answer with a JSON object.", "Say hello in the field greeting.", map[string]any{
		"type": "object", "properties": map[string]any{"greeting": map[string]any{"type": "string"}},
		"required": []string{"greeting"}, "additionalProperties": false,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", cl.Name(), err)
	}
	return fmt.Sprintf("%s ok in %s: %s", cl.Name(), time.Since(start).Round(time.Millisecond), strings.TrimSpace(string(out))), nil
}

// providerIDs is "anthropic|openai|…" for help texts.
func providerIDs() string {
	var ids []string
	for _, p := range llm.Providers {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, "|")
}

// summarizerLine describes the summarizer for doctor and status.
func (a *app) summarizerLine(cfg config.Config) string {
	sc := cfg.Summarize
	if !sc.UsesModel() {
		return "heuristic (local; nothing is sent to a model)"
	}
	c := a.summarizer(cfg)
	_, src := a.summarizerKey(sc.Provider)
	line := fmt.Sprintf("%s, API key: %s", c.Name(), src)
	if p, _ := llm.Lookup(sc.Provider); p.BaseURL != "" {
		line += ", endpoint " + firstNonEmptyStr(sc.BaseURL, p.BaseURL)
	}
	return line
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

const modelNotice = `The redacted conversation of each session in the projects you share (your
messages and the assistant's replies, after secret and path redaction) will
be sent to %s to write its summary. Tool output and file contents are never
sent. Sessions of projects you do not share never reach it. Previews only show
summaries already made.`

func (a *app) summarizerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "summarizer", Short: "Choose how session summaries are written (local heuristic, or an opt-in model)"}

	cmd.AddCommand(&cobra.Command{
		Use: "status", Short: "Show the summarizer and where its API key comes from (no network)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Stdout, a.summarizerLine(cfg))
			return nil
		},
	})

	var model, baseURL string
	use := &cobra.Command{
		Use:   "use heuristic|" + providerIDs(),
		Short: "Switch the summarizer; a model is opt-in and receives the redacted conversation of shared projects",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			p := args[0]
			if p == "heuristic" {
				cfg.Summarize = config.Summarize{}
			} else if _, ok := llm.Lookup(p); ok {
				cfg.Summarize.Provider, cfg.Summarize.Model, cfg.Summarize.BaseURL = p, model, baseURL
			} else {
				return fmt.Errorf("unknown summarizer %q: use heuristic|%s", p, providerIDs())
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := config.Save(a.cfgPath(), cfg); err != nil {
				return err
			}
			fmt.Fprintln(a.Stdout, "summarizer: "+a.summarizerLine(cfg))
			if cfg.Summarize.UsesModel() {
				fmt.Fprintf(a.Stdout, "\n"+modelNotice+"\n", a.summarizer(cfg).Name())
				if pr, _ := llm.Lookup(p); !pr.KeyOptional {
					if _, src := a.summarizerKey(p); src == "none" {
						hint := "run `goldengate summarizer set-key " + p + "`"
						if pr.EnvKey != "" {
							hint += " or set " + pr.EnvKey
						}
						if p == llm.ProviderAnthropic {
							hint += ", or `ant auth login`"
						}
						fmt.Fprintln(a.Stdout, "\nNo API key found: "+hint+".")
					}
				}
			}
			return nil
		},
	}
	use.Flags().StringVar(&model, "model", "", "model id (default: the provider's, e.g. "+llm.DefaultAnthropicModel+")")
	use.Flags().StringVar(&baseURL, "base-url", "", "endpoint override for OpenAI-compatible providers (custom: default http://localhost:11434/v1)")
	cmd.AddCommand(use)

	var del bool
	setKey := &cobra.Command{
		Use: "set-key " + providerIDs(), Short: "Store a summarizer API key in the OS keyring (input hidden)", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			p := args[0]
			if _, ok := llm.Lookup(p); !ok {
				return fmt.Errorf("unknown provider %q: use %s", p, providerIDs())
			}
			store := a.secretStore()
			if del {
				if err := store.Delete(summarizerSecret(p)); err != nil && !errors.Is(err, secrets.ErrNotFound) {
					return err
				}
				fmt.Fprintln(a.Stdout, "deleted the stored "+p+" key")
				return nil
			}
			if a.ReadSecret == nil {
				return errors.New("no terminal to read the key from")
			}
			key, err := a.ReadSecret(p + " API key: ")
			if err != nil {
				return err
			}
			if key = strings.TrimSpace(key); key == "" {
				return errors.New("no key entered")
			}
			if err := store.Set(summarizerSecret(p), key); err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "stored the %s key in the %s\n", p, store.Kind())
			return nil
		},
	}
	setKey.Flags().BoolVar(&del, "delete", false, "remove the stored key instead")
	cmd.AddCommand(setKey)

	cmd.AddCommand(&cobra.Command{
		Use: "test", Short: "Send one tiny sample (not your data) to the configured model to check it works", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			msg, err := a.testSummarizer(c.Context(), cfg)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Stdout, msg)
			return nil
		},
	})
	return cmd
}
