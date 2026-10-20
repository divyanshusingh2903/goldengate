# Security

Status: working draft. The full hardening checklist (DESIGN.md section 11) is completed in M6, before any public release.

## Reporting a vulnerability
Use GitHub private vulnerability reporting on this repository (channel to be confirmed before the first public release). Please do not open public issues for security problems.

## What the system promises
- **Default deny.** Nothing is sent until you select a project. A deny rule always beats an allow rule.
- **One door.** Every payload leaves through `sync.Push`, which validates it first. Sessions from unselected projects are dropped before they are even parsed, and are never named in logs.
- **Previews are exact.** `goldengate sync --dry-run` prints the payloads that would be sent and records nothing.
- **Redaction is best-effort.** Secret patterns, high-entropy strings and absolute paths are scrubbed before and after summarization. Regexes miss things. Use `titles-only` for sensitive projects and read the preview.
- **No telemetry.** Neither binary makes network calls other than to the server you configured.

## Tokens
| Scope | Can | Cannot |
|---|---|---|
| `admin` | mint, list and revoke tokens | read or write work data |
| `ingest` | write, list and delete work data | use the MCP endpoint, manage tokens |
| `read` | use the MCP endpoint | write, delete, manage tokens |

- The admin secret is `GOLDENGATE_ADMIN_TOKEN` (at least 32 random bytes; weak values stop the server from starting). It is compared in constant time and is never stored or logged. `goldengate init` keeps it in memory only.
- Minted tokens are 256-bit random values with a prefix (`ggb_read_`, `ggb_ingest_`) so secret scanners can find leaks. Only their SHA-256 hash is stored. They are shown once, can expire, and can be revoked: a revoked token is refused on its next request.
- Rotation (`goldengate token rotate`) mints a replacement and revokes the old tokens, optionally after a grace period.
- Pairing codes are 60 bits, single use, expire (10 minutes by default), and every failed exchange counts against all live codes, which are burned after 5 failures.
- Repeated bad tokens lock an address out for a few minutes. Per-address and per-token rate limits apply. Failure counters and limits are held in memory per instance: on a multi-instance deployment each instance counts separately.
- Behind a TLS-terminating proxy, run with `--behind-tls-proxy` (and `--proxy-hops`). Without it `X-Forwarded-For` is ignored, so it cannot be used to dodge the lockout.
- The server refuses to start without credentials, and refuses plain HTTP on a non-loopback address unless TLS is configured or `--behind-tls-proxy` is given. The client refuses plain `http://` servers except on loopback and never follows redirects.
- The audit log records token id, scope, action, project key, time and source address. Never payload content. Default retention is 90 days.

## OAuth (optional, `serve --oauth`)
Some assistants only connect through OAuth. With `--oauth --public-url https://your-host` the bridge also runs a small single-user OAuth 2.1 authorization server (MCP authorization spec: RFC 9728 and 8414 discovery, RFC 7591 dynamic registration, PKCE S256 only, RFC 8707 resource indicators, exact redirect-URI matching).

- **Who can approve:** only the holder of the admin secret. The consent page shows the application's name and exactly where it will be sent, and asks for the admin secret. Wrong guesses go through the same lockout as every other token check.
- **What a connected assistant gets:** an ordinary short-lived (1 hour) `read` token plus a rotating refresh token (30 days). It can never write, delete or manage tokens.
- **Theft handling:** authorization codes are single use; reusing a code, or replaying an already-used refresh token, revokes everything issued from that grant. `goldengatebridge oauth clients delete ID` cuts a client off immediately.
- **Open registration:** anyone who can reach the server can register a client, but a registered client gets nothing without your consent. Registration is rate limited and capped, unused clients are pruned after 7 days, and `--oauth-redirect-host` can restrict redirect URIs to the hosts you expect.
- **The phishing risk to know about:** an attacker can register a client with their own redirect address and lure you to its sign-in link. The consent page names the application and its redirect host so you can check; approve only connections you started yourself, and prefer `--oauth-redirect-host` once you know your assistant's address.
- **Browser surface:** the consent page is server-rendered (`html/template`, no JavaScript), is served with a strict CSP, `X-Frame-Options: DENY`, `no-store` and no referrer, and only ever carries an unguessable one-time request id, never the request parameters. Discovery metadata is the only CORS-enabled response.
- Without `--oauth` none of these endpoints exist.

## The interactive UI
The UI cannot do anything the commands cannot: it runs on the same code, so selection, redaction, validation and the single push path apply unchanged. Sync starts as a dry run, which goes through validation but sends and records nothing. The admin token typed into the setup is masked, used for the one connection, and wiped from memory-backed fields afterwards; it is never written down or displayed. Previewing reads and summarizes sessions locally and writes nothing. The state database keeps the last pushed payload of each session (the already-redacted text the server received) so the UI can show what changed; it lives next to the config with mode 0600.

## Sources and imports
- **Coding tools** (Claude Code, Codex, OpenCode) are read in place and read-only. Only session history is opened: for OpenCode that means four tables of its database (a test enforces the allow-list); its credential and account tables, `auth.json` and every other file next to it are never touched.
- **Chat exports** (claude.ai, ChatGPT) are zips you supply. They are untrusted input: the whole archive is refused if any entry name could escape a directory (`..`, absolute paths, backslashes, drive letters, control characters), is a symlink or a duplicate, or if the archive or an entry is suspiciously large or compressible (zip bombs). Nothing is ever extracted to disk; decompression is bounded even if the archive lies about sizes. Only the conversation files are opened (`conversations.json`, `projects.json` for names, and ChatGPT's numbered shards): account files (`users.json`, `user.json`), `chat.html`, feedback and shared-link files are never read.
- **Chat history is more personal than code history**, so chat projects (`claude-ai:Chats`, `chatgpt:project-...`) are shared only by an explicit rule, even when `share.default = "allow"`.
- **Artifacts and uploads** from chats travel as metadata only (id, title, status): never their contents. Names that look like secrets (`.env`, keys) are dropped.
- **Worktrees** are filed under their repository only when the repository itself is selected, so the name of an unselected folder is never sent. Finding worktrees uses folder paths and what the tool itself records; reading a worktree's `.git` pointer file is opt-in (`enrich.git`).
- **The chat export layouts are not formally documented.** The parsers follow the commonly seen shapes, skip anything unknown with a warning, and were tested on synthetic fixtures, not on real exports.

## Prompt injection (read this)
Everything the server stores comes from coding-session transcripts, which include text from web pages, READMEs, issue bodies and tool output. Any of it may contain instructions aimed at an AI model. When another assistant reads it over MCP, that text is **untrusted data**.

What the server does about it:
- Tool descriptions are static and never contain stored data.
- Text results wrap stored strings between markers carrying a random per-response nonce, and say up front that the content is data, not instructions. Stored `<<<` and `>>>` runs are neutralized so stored text cannot forge a marker.
- Structured output carries a `notice` field with the same warning.
- Ingest rejects control characters, bidirectional overrides and zero-width characters, caps every field, and only accepts plain `https://` URLs without credentials.
- Every tool is read-only and annotated so.

What it cannot do: stop a model from obeying text it was shown. This reduces the risk, it does not eliminate it. Do not connect an assistant that has dangerous tools (shell, email, payments) to this server without a human approving each action, and keep sensitive projects at `titles-only` or unshared.

## Residual risks
- A compromised server leaks the stored summaries. Prefer `titles-only` for sensitive projects.
- The `read` token lives in your assistant vendor's connector settings.
- Regex redaction will occasionally miss secrets.
- Without an OS keyring the ingest token is kept in a `0600` file that is **not encrypted**; `goldengate` says so when it falls back.
- SQLite data is not encrypted at rest by the application; use disk encryption.
