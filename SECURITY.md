# Security

Status: working draft. The full hardening checklist (DESIGN.md section 11) is completed in M6, before any public release.

## Reporting a vulnerability
Use GitHub private vulnerability reporting on this repository (channel to be confirmed before the first public release). Please do not open public issues for security problems.

## What the system promises
- **Nothing is shared by default.** Nothing is sent until you select a project. Blocking a folder (`goldengate share deny`) always wins over sharing it.
- **One door.** Every payload leaves through `sync.Push`, which validates it first. Sessions from unselected projects are dropped before they are even parsed, and are never named in logs.
- **Previews are exact.** `goldengate sync --dry-run` prints the payloads that would be sent and records nothing.
- **Nothing new leaves without approval.** Sessions of a project you share wait in an inbox until you approve them, and a session that changes after approval waits again. Unattended sync (`goldengate sync --auto`, which the hook and the timer run) sends only what you approved or your own rules approve, cannot be told to approve what waits, says what it left in the inbox, and lists everything it sent in a receipt. The installers write only the binary's path and that command into the scheduler or into Claude Code's settings, never a token. The optional background daemon (`goldengate install-service`, off until you say yes) runs exactly that command when something changed; it opens no network port, reads only the folders you share (it lists their sessions to see what changed, and reads one only to send it), and a session waiting in the inbox stays there. Automation is a per-project choice you make yourself: a trust level ("ask" by default, "suggest", or "auto") and rules you write. Projects shared before the inbox existed keep "auto" so their behaviour did not change.
- **Rules only narrow, inside what you share.** A rule belongs to a project and acts only on that project's sessions; it cannot share a folder or chat you did not share, and a block always wins, before any session is read. Rules match on facts recorded by your tools (tool, folder, repository remote, branch, issue and pull request keys), never on text a model wrote. Even at "auto", the first session from a folder or chat grouping you did not name yourself waits for you. An exclude rule keeps its matches on your machine and is not overridden by `sync --approve`.
- **Suggestions never read what you did not share.** When a folder with sessions is in no project, Golden Gate may suggest one. It does this from facts about the folder (a worktree of a shared repository; with `enrich.git` on, the repository's remote and current branch, read from two git metadata files), never from the folder's sessions. Nothing is shared until you accept, and accepting sends nothing by itself.
- **Offers are only offers.** After you send several sessions from the same folder by hand, Golden Gate may offer the rule that would have sent them. It shows the exact rule and changes nothing unless you say yes; "not now" is remembered. Offers are worked out on your machine from the record of what you sent, with no model involved.
- **Automatic sends leave a receipt.** Every session a project's trust level and rules sent without asking you is listed in a receipt on your machine: which session, which project, and which rule sent it. `goldengate receipts` shows them and retracts any item. The receipt holds only what the server received (ids, project names, redacted titles). The optional desktop notification is off by default and carries counts only.
- **Taking a session back.** `goldengate sessions retract <id>` (or `R` in the inbox) deletes a sent session from the server, with its work-item links, and records that it must never be sent again. `sessions exclude` keeps a session from ever being sent. Both are checked before a session is read, so an excluded or retracted session appears in no payload, no dry run and no model request. Sessions that are still waiting for approval are not sent to the model summarizer.
- **Your edits are yours, and still redacted.** A summary you edit is sent as you wrote it, after the same secret and path redaction as generated text, and the preview shows it exactly. No summarizer replaces it. If the session later continues, it is held rather than sent with either your old text or a new generated summary, until you decide. The edit form is a file readable only by you, removed when you close the editor.
- **Redaction is best-effort.** Secret patterns, high-entropy strings and absolute paths are scrubbed before and after summarization. Regexes miss things. Use `titles-only` for sensitive projects and read the preview.
- **No telemetry.** Neither binary makes network calls other than to the server you configured, and, only if you turn it on, the summarizer model you chose.

## The model summarizer (opt-in)
By default summaries are written locally by fixed rules and no model is involved. If you choose a model (Settings → Summarizer, which asks first, or `goldengate summarizer use anthropic|openai|gemini|opencode|custom`), each session of a **shared** project is summarized by that model:
- It receives the session's conversation (your messages and the assistant's replies) after the same secret and path redaction as everything else, with each message capped and long sessions cut in the middle. Tool output, file contents and command arguments are never sent. Sessions of projects you do not share never reach it.
- Previews never call the model; they show a model summary only if one was already made during a sync.
- The answer is constrained to a JSON schema, redacted again, capped, and stored as plain text; it reaches the server only through the same validated path as every payload.
- With a model on, the summary carries a **narrative**: a few paragraphs of prose (up to about 4000 characters at the "detailed" length) rather than a line or two, so more of what was discussed reaches the server than with the local rules. The model is told to leave out code, command arguments, tool output, secrets and the text of issue descriptions and comments; code blocks are stripped and the narrative is redacted again and capped on the laptop, and the server refuses one over its cap. The preview shows the full narrative before anything is sent. Assistants get a short abstract in list tools and the full narrative only from `get_session` and `get_work_item` (which returns the sessions linked to one issue or pull request).
- That provider sees the redacted text under its own terms. With an OpenAI-compatible endpoint on this machine (Ollama, LM Studio) nothing leaves it.
- API keys are typed hidden in the TUI or `goldengate summarizer set-key` and stored in the OS keyring, or read from the provider's variable (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `OPENCODE_API_KEY`, `GOLDENGATE_SUMMARIZER_KEY`); they are never written to the config or shown.
- Some free models on gateways such as OpenCode Zen retain data or are for trial use; read the provider's terms before sending work there.
- Transcripts may contain text that tries to instruct the model. The prompt tells it the conversation is data; the worst case is a misleading summary, since the model has no tools and its output is only stored text.

## Tokens
| Token | Server scope | Can | Cannot |
|---|---|---|---|
| Admin secret | `admin` | create, list and revoke tokens | read or write work data |
| Upload token (this computer) | `ingest` | write, list and delete work data | use the MCP endpoint, manage tokens |
| Assistant token | `read` | use the MCP endpoint | write, delete, manage tokens |

- The admin secret is `GOLDENGATE_ADMIN_TOKEN` (at least 32 random bytes; weak values stop the server from starting). It is compared in constant time and is never stored or logged. `goldengate init` keeps it in memory only.
- Minted tokens are 256-bit random values with a prefix (`ggb_read_`, `ggb_ingest_`) so secret scanners can find leaks. Only their SHA-256 hash is stored. They are shown once, can expire, and can be revoked: a revoked token is refused on its next request.
- Rotation (`goldengate token rotate`) mints a replacement and revokes the old tokens, optionally after a grace period.
- Pairing codes are 60 bits, single use, expire (10 minutes by default), and every failed exchange counts against all live codes, which are burned after 5 failures.
- Repeated bad tokens lock an address out for a few minutes. Per-address and per-token rate limits apply. Failure counters and limits are held in memory per instance: on a multi-instance deployment each instance counts separately.
- Behind a TLS-terminating proxy, run with `--behind-tls-proxy` (and `--proxy-hops`). Without it `X-Forwarded-For` is ignored, so it cannot be used to dodge the lockout.
- The server refuses to start without credentials, and refuses plain HTTP on a non-loopback address unless TLS is configured or `--behind-tls-proxy` is given. The client refuses plain `http://` servers except on loopback and never follows redirects.
- `/mcp` refuses a request whose `Origin` header names a browser origin it does not trust (403, before the token is checked), so a web page cannot reach it by DNS rebinding. Only the `--public-url` origin and origins given with `--allowed-origin` are trusted; requests without an `Origin` header, which is how assistants and CLIs connect, are unaffected. The MCP library's own loopback Host check is switched off, because a reverse proxy on the same machine (or a container runtime that proxies over loopback) reaches the server over loopback with the public Host; the Origin list and the bearer token are what stop a rebinding page.
- The audit log records token id, scope, action, project key, time and source address. Never payload content. Default retention is 90 days.

## OAuth (optional, `serve --oauth`)
Some assistants only connect through OAuth. With `--oauth --public-url https://your-host` the bridge also runs a small single-user OAuth 2.1 authorization server (MCP authorization spec: RFC 9728 and 8414 discovery, RFC 7591 dynamic registration, PKCE S256 only, RFC 8707 resource indicators, exact redirect-URI matching).

- **Who can approve:** only the holder of the admin secret. The consent page shows the application's name and exactly where it will be sent, and asks for the admin secret. Wrong guesses go through the same lockout as every other token check.
- **Per-agent access:** every `read` token and OAuth client belongs to a named agent, and an agent sees only the projects granted to it. Nothing is granted automatically: a project you start sharing is visible to no assistant until you grant it. The filter is applied inside the server's queries for every tool, an ungranted project is indistinguishable from one that does not exist, and a grant or revocation applies to the very next request. Readers that existed before this feature were moved to a `Default` agent that kept the projects it could already see.
- **Who may change access:** only the admin secret can create agents, grant or revoke projects, or create an agent's assistant token (`goldengate agents ...` prompts for it or reads `GOLDENGATE_ADMIN_TOKEN`, and never stores it). The laptop's `ingest` token cannot, so a leaked ingest token cannot widen what any assistant sees. The cost is a prompt each time you change access. The admin listing shows project ids and names and agent names, never session content.
- **The Agents screen** in the terminal UI asks for the admin secret once, keeps it in memory until you quit (`L` forgets it sooner), and never writes it to the config, the keyring or a log. "View as agent" never asks the server for work data with the admin secret; it is computed on your machine from what this computer synced.
- **The first assistant:** when `goldengate init` connects to a server that has no agents yet, the assistant token it creates belongs to the `Default` agent, which is set to see every project you share, and the setup tells you so. Turn that off with `goldengate agents revoke Default --all`. Agents you add later see nothing until you grant projects, and no agent is given "every project" unless you ask for it.
- **What a connected assistant gets:** an ordinary short-lived (1 hour) `read` token plus a rotating refresh token (30 days). It can never write, delete or manage tokens.
- **Theft handling:** authorization codes are single use; reusing a code, or replaying an already-used refresh token, revokes everything issued from that grant. `goldengatebridge oauth clients delete ID` cuts a client off immediately.
- **What it may see is chosen at approval:** once the admin secret is accepted, a second page asks which assistant this is (a new agent with a name, or an existing one) and which projects it may see. Nothing is ticked: an application you approve without ticking anything sees nothing until you grant projects. For an existing agent, ticks only add access. The first page is open to anyone who has the link, so it shows no agent or project names; those appear only on the second page, which is bound to a fresh single-use id issued after the secret was verified.
- **Open registration:** anyone who can reach the server can register a client, but a registered client gets nothing without your consent. Registration is rate limited and capped, unused clients are pruned after 7 days, and `--oauth-redirect-host` can restrict redirect URIs to the hosts you expect.
- **The phishing risk to know about:** an attacker can register a client with their own redirect address and lure you to its sign-in link. The consent page names the application and its redirect host so you can check; approve only connections you started yourself, and prefer `--oauth-redirect-host` once you know your assistant's address.
- **Browser surface:** the consent page is server-rendered (`html/template`, no JavaScript), is served with a strict CSP, `X-Frame-Options: DENY`, `no-store` and no referrer, and only ever carries an unguessable one-time request id, never the request parameters. Discovery metadata is the only CORS-enabled response.
- Without `--oauth` none of these endpoints exist.

## The interactive UI
The UI cannot do anything the commands cannot: it runs on the same code, so selection, redaction, validation and the single push path apply unchanged. Sync starts as a dry run, which goes through validation but sends and records nothing. The admin token typed into the setup is masked, used for the one connection, and wiped from memory-backed fields afterwards; it is never written down or displayed. Previewing reads and summarizes sessions locally and writes nothing. The state database keeps the last pushed payload of each session (the already-redacted text the server received) so the UI can show what changed. It also keeps a plan cache: the already-redacted payload that an offline plan built for each session of a shared project, including sessions you have not sent yet, so opening the inbox does not read every transcript again. Nothing in it leaves the machine, a row is removed when its session leaves the plan or its project is forgotten, and a send never reads it. The database lives next to the config with mode 0600.

## The local web UI (`goldengate ui`)
`goldengate ui` shows Golden Gate in your browser. The model is DESIGN.md section 6.1. What holds:

- It listens on loopback only, on a random port, and there is no option to listen anywhere else. It runs only while the command runs.
- It opens with a single-use launch token that expires in two minutes and never appears in a request line or a log. After that, every request needs both a session cookie (`HttpOnly`, `SameSite=Strict`) and a request token held in the page's memory, and passes exact Host and Origin checks. There is no CORS.
- It is a front end on the same code as the TUI and the commands: it cannot share, send or change anything they cannot, and everything still leaves through the one push path.
- Secrets can be set from the page and are never sent back to it. The one exception is a value you must copy elsewhere, shown once when it is created (the assistant token at the end of setup), as in the terminal.
- The page loads nothing from any other site, and shows session text as text, never as markup.

## Sources and imports
- **Cursor** keeps conversations in a database that also holds its sign-in state. Golden Gate opens it read-only and reads only the conversation list, the messages and one version entry; tests plant decoy sign-in values and keys there and check that they never surface.
- **Antigravity** keeps sign-in tokens next to its conversations. Golden Gate opens its two databases read-only (the conversation list, and the branch record of a conversation it sends) and reads conversation transcripts; listing only checks a transcript's size and time, so the folders you do not share are not read. Nothing else in `~/.gemini` is opened.
- **Coding tools** (Claude Code, Codex, OpenCode, Cursor, Antigravity) are read in place and read-only. Only session history is opened: for OpenCode that means four tables of its database (a test enforces the allow-list); its credential and account tables, `auth.json` and every other file next to it are never touched.
- **Chat exports** (claude.ai, ChatGPT) are zips you supply. They are untrusted input: the whole archive is refused if any entry name could escape a directory (`..`, absolute paths, backslashes, drive letters, control characters), is a symlink or a duplicate, or if the archive or an entry is suspiciously large or compressible (zip bombs). Nothing is ever extracted to disk; decompression is bounded even if the archive lies about sizes. Only the conversation files are opened (`conversations.json`, `projects.json` for names, and ChatGPT's numbered shards): account files (`users.json`, `user.json`), `chat.html`, feedback and shared-link files are never read.
- **Chats are shared one conversation at a time.** `goldengate chats share <id>` shares that conversation and nothing else of its export; the other conversations are not selected and a sync never reads them. Listing your conversations (`goldengate chats`) shows their titles on your machine and sends nothing.
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
- The server's upload endpoint rejects control characters, bidirectional overrides and zero-width characters, caps every field, and only accepts plain `https://` URLs without credentials.
- Every tool is read-only and annotated so.

What it cannot do: stop a model from obeying text it was shown. This reduces the risk, it does not eliminate it. Do not connect an assistant that has dangerous tools (shell, email, payments) to this server without a human approving each action, and keep sensitive projects at `titles-only` or unshared.

## Residual risks
- A compromised server leaks the stored summaries. Prefer `titles-only` for sensitive projects.
- The `read` token lives in your assistant vendor's connector settings.
- Regex redaction will occasionally miss secrets. With the model summarizer on, a missed secret reaches that provider as well as the server.
- The daemon is a long-running process of your user that holds the upload token while it runs. A service has no desktop session, so on some systems it cannot reach the keyring; `goldengate daemon --check` (run by `install-service`) says so and what to do, and the file fallback for the token is unencrypted (see the next line).
- Without an OS keyring the upload token is kept in a `0600` file that is **not encrypted**; `goldengate` says so when it falls back.
- SQLite data is not encrypted at rest by the application; use disk encryption.
