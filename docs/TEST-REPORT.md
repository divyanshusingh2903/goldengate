# End-to-end test report (2026-11-17)

Golden Gate was driven by hand, end to end, in the TUI, the web UI (`goldengate ui`) and the CLI, against a local `goldengatebridge`. The aim was to show that the product works as DESIGN.md 1.1 says, and to fix what does not. Each bug found was filed as an issue and fixed in its own PR through the ledger loop (`docs/LEDGER.md`, "Bug fixes from end-to-end testing").

## Setup

- **Binaries:** both built fresh from `main` at the start (`b7a727a`).
- **Isolation:** a temporary `HOME` with its own `XDG_CONFIG_HOME` and `XDG_STATE_HOME`. `DBUS_SESSION_BUS_ADDRESS` pointed nowhere, so the secret store fell back to its file, and no real keyring was touched.
- **Data:** synthetic only, built from `testdata`:
  - Claude Code and Codex JSONL;
  - OpenCode and Cursor databases;
  - claude.ai and ChatGPT export zips.
  Paths were rewritten into the temporary home and timestamps moved to recent days. Fake secrets were planted in prompts and commands: an AWS key pair, a GitHub token, and home paths. No real session data was read or sent.
- **Server:** `goldengatebridge serve` on loopback with its own database and a generated admin token, later restarted with `--oauth`.
- **Proxy:** a logging proxy sat in front of the server. It recorded every upload body, so the dry run could be compared with what was really sent, and it could return 503 on demand to stand in for a server that is down.
- **How each UI was driven:** the TUI through tmux (keys sent, panes captured); the web UI in a real browser (the desktop app's browser pane); MCP with a small JSON-RPC client over streamable HTTP.

## Results by flow

P = pass, F = failed and fixed (PR below), – = not applicable to that front end.

| # | Flow | TUI | Web | CLI |
|---|---|---|---|---|
| 1 | Setup and pairing | F → P (#117, #118, #124) | P | P |
| 2 | Projects | F → P (#113, #116, #121) | F → P (#113, #116, #122) | F → P (#114) |
| 3 | Inbox | F → P (#119) | P | F → P (#119) |
| 4 | Rules, offers, receipts | P | P | P |
| 5 | Preview and send, dry run, deny beats allow, redaction | F → P (#112) | F → P (#112) | F → P (#112) |
| 6 | Server screen, token rotation, delete | F → P (#124) | P | F → P (#124) |
| 7 | Agents and grants | F → P (#120) | P | P |
| 8 | MCP read tools, windows, no write tools, OAuth | – | – | F → P (#115) (server) |
| 9 | Settings | F → P (#124) | F → P (#123) | P |
| 10 | Edge cases | F → P (#118) | P | P |

### Evidence

**1. Setup and pairing**
- The first-run wizard detects every source and connects with the admin token, which is then forgotten.
- The assistant token is shown once, and the `Default` agent sees every shared project.
- Web pairing: a pairing code works once, and a second use is refused.
- Bugs fixed:
  - Esc from a preview opened in setup left setup (#117).
  - The no-keyring warning was printed over the setup screen (#118).
  - Token names did not match the glossary (#124).

**2. Projects**
- Share, block and undo work, and a tree share and per-tool sharing behave as described.
- A block (deny) wins over a `~` (partial) share.
- Unsharing a worktree deletes only that worktree's sessions from the server.
- Bugs fixed:
  - Putting a partly shared folder into a project switched its tools back on (#113).
  - Sharing a parent folder from the CLI left child projects tombstoned, so their sessions were dropped without a word (#114).
  - Excluded and retracted sessions were counted as "to send" (#116).
  - TUI search matched letters of the hidden home directory (#121).
  - Web warnings showed the home path and overflowed at phone width (#122).

**3. Inbox**
- Approve, exclude, undo and edit all work. An edited summary is redacted, and in the web UI an XSS string is rendered as text.
- An edit conflict can be resolved with "keep" (TUI) or "use new" (web). Approve all works.
- Retract works from the CLI, from receipts and from the web UI: the session is deleted on the server first, then recorded locally.
- Bug fixed (#119): a held edit conflict was not mentioned by an interactive `sync`, and `a` in the TUI claimed a held session would go out.

**4. Rules, offers, receipts**
- A trust level of `auto` plus rules sends without asking, and every automatic send is listed in a receipt naming its rule.
- A broad member asks before the first send from a new folder; after that, the trust level decides.
- The rule offer appears after repeated manual approvals, and the project suggestion offer appears for an unassigned worktree.

**5. Preview and send**
- The dry-run payloads were JSON-identical to the bodies the proxy recorded for the real send: 7 of 7.
- Planted secrets never reached the server: AWS keys, GitHub token, and `SECRET-` strings in the exports.
- While the server was down, a send failed with a clear 503 message, and retrying succeeded (CLI and web).
- Bug fixed (#112): an absolute path containing a random-looking folder was only partly redacted. The entropy pass split the path, and its tail (home directory, user name, client folder) was sent.

**6. Server screen**
- Rotating the assistant token and the upload token worked. The old tokens were revoked and the new upload token was stored.
- Delete from server worked from both the TUI and the web UI.
- Bug fixed (#124): wording ("Ingest token", "Created a new ingest token").

**7. Agents and grants**
- The projects-by-agents grid works in both UIs, with review before apply.
- View as agent, token revoke and "forget the admin secret" all work.
- Grants are per agent, and a new agent sees nothing.
- Bug fixed (#120): TUI view-as cut colour codes in half, so the frame was drawn mid-line.

**8. MCP and OAuth**
- 10 tools are listed, all annotated read-only, and there are no write tools.
- Scope isolation: an upload token or a revoked token gets 401/403.
- The time window defaults to 5 days. `days`, `since` and `until` work, and windows are capped at 365 days.
- An ungranted project looks the same as a missing one.
- OAuth: two-step consent (admin secret, then agent and projects, with nothing ticked) and PKCE. Refresh works. Only the granted scope is readable. Replaying an authorization code revokes the tokens issued from it.
- Bug fixed (#115): the `recent_activity` text listed sessions under the wrong project heading when projects interleaved (the structured output was right).

**9. Settings**
- Share level, look-back and tools can be changed. An extra redaction pattern is applied in the preview.
- The summarizer provider, model and length can be set. The key is write-only.
- Theme toggle works.
- Bugs fixed:
  - The web UI said the key was in the keyring when it was in the file fallback (#123).
  - The TUI said the session hook "is not available yet", although `install-hook` and `install-schedule` exist (#124).

**10. Edge cases**
- Empty states (no sessions, no server) work.
- Web UI security:
  - Reloading ends the session, and an old launch link returns 403.
  - Wrong `Host` and `OPTIONS` requests are refused, and the security headers are present.
  - The server listens on loopback only.
- Imports: claude.ai and ChatGPT exports import, and their chats are shared per conversation.
- A server that is down: covered in flow 5.

## Bugs found and fixed

| Issue | What | PR |
|---|---|---|
| #112 | Redaction: a random-looking folder split a path and its tail was sent | #125 |
| #113 | Moving a partly shared folder into a project switched its tools back on | #126 |
| #114 | CLI share of a parent left child projects tombstoned | #127 |
| #115 | MCP `recent_activity` text listed sessions under the wrong project | #128 |
| #116 | Excluded and retracted sessions counted as to send | #129 |
| #117 | TUI setup: Esc from a preview left setup | #130 |
| #118 | TUI: no-keyring warning printed over the interface | #131 |
| #119 | Held edit conflicts: sync said nothing, TUI approve claimed they go out | #132 |
| #120 | TUI view-as: colour codes cut mid-sequence | #133 |
| #121 | TUI search matched the hidden home directory | #134 |
| #122 | Web UI warnings: home path, overflow at phone width | #135 |
| #123 | Web UI settings: wrong key store label | #136 |
| #124 | Words: ingest/read token, "sent" for shared, stale hook text | #137 |

Each fix has a regression test that fails on the old code (#123's backend test passes on both; there the bug was in the page, and the asset test covers it). Each PR passed `make check` and `govulncheck ./...`.

## Seen but not fixed (small; for a later pass or the redesign)

- After "delete from server", a session that was approved is queued to be sent again.
- The receipts screen's intro ("sent without asking") reads oddly next to items the user approved in the inbox.
- The web Agents screen asks for the admin secret even when no server is configured.
- The TUI empty state does not mention Cursor or chat exports.
- The TUI tree card shows the folder's absolute path instead of the `~` form.
- After a summary is edited, the TUI inbox row keeps the generated title.
- A project's "Rule" line shows only the first excluded tool.
- After a new redaction pattern is added, the preview shows a diff for a session that did not otherwise change.

## Not tested, and why

- **Phone width below about 380px:** the headless browsers on this machine (snap Chromium and Firefox) cannot run headless, and the browser pane's emulation goes down to about 380px only. #122's overflow was found and fixed at that width.
- **A real OS keyring:** the test kept away from the user's keyring on purpose. Only the file fallback was used, and the keyring path is covered by unit tests.
- **Installed hooks and timers:** `install-hook` and `install-schedule` were checked with `--print` only, so that nothing was installed on the real system. `sync --auto` itself was tested.
- **Remote model summarizers:** no API key was used. The local rules wrote every summary, and the provider settings were checked only as far as saving them and "List models" errors.
- **Windows and macOS:** Linux only.
- **Cursor on a real installation:** the Cursor adapter read the testdata database only.
