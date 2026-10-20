# Golden Gate

Let a remote AI assistant know what you have been working on in your coding tools, with **you choosing which projects are shared**.

- `goldengate` (laptop): reads your local session history, applies your allow/deny choices, redacts and summarizes, and pushes to your own server.
- `goldengatebridge` (server you run): stores the summaries and serves them read-only over MCP (streamable HTTP, stateless JSON).

Status: pre-release (M2). Claude Code is supported as a source; Codex, OpenCode, the TUI, deployment scripts and OAuth are on the roadmap in [DESIGN.md](DESIGN.md). See [SECURITY.md](SECURITY.md) before connecting an assistant.

## Quickstart (about ten minutes)

1. **Run the server** behind HTTPS. Generate the admin secret once and keep it:

   ```bash
   export GOLDENGATE_ADMIN_TOKEN="$(openssl rand -base64 32)"
   goldengatebridge serve --addr :8080 --behind-tls-proxy --db /data/bridge.db
   ```

2. **Connect your laptop.** Paste the admin secret when asked (it is never written to disk):

   ```bash
   goldengate init --server https://bridge.example.com
   ```

   This stores an `ingest` token in your OS keyring and prints a `read` token **once**. On a server where you have a shell you can pair instead: run `goldengatebridge pair` there and `goldengate init --server URL --pair-code CODE` here.

3. **Choose what to share, preview, sync:**

   ```bash
   goldengate share add ~/code/myproject
   goldengate sync --dry-run     # exactly what would be sent
   goldengate sync
   ```

4. **Connect the assistant** with `https://bridge.example.com/mcp` and the read token, as an `Authorization: Bearer` header if its connector offers one. Some assistants (Gemini's consumer app appears to be one) only support OAuth, which is not implemented yet.

Un-share with `goldengate share remove ~/code/myproject --purge-remote`. Rotate the assistant's token with `goldengate token rotate read`.

## Development

```bash
make check          # vet, race tests, staticcheck, golangci-lint
govulncheck ./...
```

The pre-commit hook (`git config core.hooksPath .githooks`) blocks secret-shaped strings, real home paths, `.env` files and raw transcripts. Test fixtures are synthetic or scrubbed; never commit real session data.

License: Apache-2.0.
