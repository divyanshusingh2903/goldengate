# Product v2 work ledger

This file lets any Claude Code session (or person) pick up the Product v2 work where the last one stopped. **Read it before starting work, and update it as part of every issue.**

- Plan and reasons: `DESIGN.md` section 1.1 (product principles) and the GitHub [Product v2 milestone](https://github.com/divyanshusingh2903/goldengate/milestone/1).
- Each issue's body is its spec: scope, rules, and "Done when". Read it in full with `gh issue view <n>` before writing code.
- Target: every issue in the queue merged by **2026-11-30**.
- Roadmap page (Claude artifact, private to the owner): https://claude.ai/artifact/8LFj2xzfz1Vm6vKamiBS3p. It shows the same order day by day, with progress. Keep it in step with this file (loop step 11).

## Status

_Last updated 2026-11-17. Update this section whenever a row in the queue changes._

**Progress:** 30 of 31 scheduled issues done (plus 2 stretch). Phases 1 (Foundations), 2 (Per-agent access) and 3 (Inbox and curation) are complete.
**Bug fixes from the end-to-end test** (#112–#124, PRs #125–#137) are all done; the report is `docs/TEST-REPORT.md`, with small observations left for later.
**Web UI restyle** (#139, PR #141) and its follow-ups #140 (state database concurrent first open, PR #142) and #143 (Receipts leaves the counts stale, PR #144) are done and #146 (Projects uses the width, tool logos, PR #147) are done; #148 (a review you can review, a readable Preview) is in progress.
**Nothing else is next:** every issue of the queue is done except #40. **Blocked:** #40 Share-link import: the #39 spike recommends not building it; whether to close it as not planned is the user's call. Phase 4 (work items, rules and automation) is complete: #21, #35, #36, #45, #31, #33, #34, #32 and #22.

### Done

| Issue | What changed | PR | Merged |
|---|---|---|---|
| #17 | DESIGN.md 1.1 product principles, 1.2 glossary, revised non-goals, M7 milestone | #50 | 2026-11-14 |
| #37 | MCP time-bounded tools default to the last 5 days; `since`/`until` on every one; results state their window; store queries gain `Until` | #55 | 2026-11-14 |
| #18 | Named projects: `[[projects]]` with members in the config (membership shares, deny still wins, one folder per project), legacy allow rules read as projects, `goldengate projects` commands, TUI `n`/`N` | #59 | 2026-11-14 |
| #49 | Summaries split into facts (local rules) and a narrative (model passages at short/standard/detailed, or the user's); separate narrative caps; MCP list tools return an abstract, `get_session` the full narrative | #60 | 2026-11-14 |
| #19 | Server files everything under stable project ids with display names; payload v2; in-place SQLite upgrade; MCP tools take a name or id; client re-files renamed or merged projects without re-summarizing | #62 | 2026-11-14 |
| #24 | Agents and grants data model: every read token and OAuth client belongs to one agent; upgrade creates `Default` with every existing project; new projects granted to no one | #63 | 2026-11-14 |
| #25 | Every MCP tool is limited to the calling agent's grants and history window, applied inside the store queries; ungranted looks like missing; audit records the agent | #64 | 2026-11-14 |
| #26 | Admin-only agents API and `goldengate agents` commands; per-agent `all_projects` switch; init makes the first assistant see every shared project | #65 | 2026-11-14 |
| #27 | OAuth consent in two steps: admin secret, then which agent and which projects (nothing ticked); the open first page shows no names | #66 | 2026-11-14 |
| #28 | TUI Agents screen: projects by agents grid, review before apply, view as agent, agent details and token revoke; four new `tui.Backend` methods | #67 | 2026-11-14 |
| #20 | Local inbox: per-session decisions, `auto_approve` per project, sync sends only approved sessions for projects under review, `goldengate inbox`, `sync --approve` | #70 | 2026-11-14 |
| #29 | `goldengate sessions list|include|exclude|retract`, TUI inbox screen (`i`), retract deletes on the server first; waiting sessions no longer reach the model | #71 | 2026-11-14 |
| #30 | Editable summaries: `goldengate sessions edit` and `e` in the TUI inbox; the edit is used instead of any summarizer, redacted, never overwritten; a session that changes after an edit is held until the user decides | #72 | 2026-11-14 |
| #21 | Issue keys read from branch names (evidence `branch`, status `mentioned`); `internal/utils/threads` groups sessions across sources by shared issues and pull requests | #74 | 2026-11-14 |
| #35 | GitLab issues and merge requests: URLs on gitlab.com and listed self-hosted hosts, `glab` results as evidence, provider `gitlab`; config version 3 | #76 | 2026-11-14 |
| #36 | `get_project_context` leads with active threads; new MCP tool `get_work_item` returns one thread with linked sessions and status history; tool descriptions say when to use each | #78 | 2026-11-15 |
| #45 | DESIGN.md 6.1: design and security model for `goldengate ui` (loopback, launch token, cookie plus request token, Host and Origin checks, write-only secrets, embedded hand-written assets), reviewed against SECURITY.md | #80 | 2026-11-15 |
| #31 | Per-project trust levels (ask, suggest, auto) and user-written rules on deterministic facts that approve or exclude a project's sessions; broad patterns ask before first sending from a new folder or chat; `goldengate rules`, `projects trust` | #82 | 2026-11-15 |
| #33 | Receipts: every automatic send is listed in one receipt with its rule; `goldengate receipts` (list, show, retract), TUI receipts screen, opt-in notification | #84 | 2026-11-15 |
| #34 | `goldengate sync --auto` for unattended runs (approved or rule-approved only, one-line report, receipt); `install-schedule` and `install-hook claude-code` with `--print` and uninstall | #86 | 2026-11-15 |
| #32 | Rule offers from repeated manual choices: exact rule, what it would have matched, yes or not now (remembered); CLI inbox, `rules offers`, TUI inbox | #88 | 2026-11-15 |
| #22 | Project suggestions for folders that are in no project, with the reason (worktree, same remote, issue branch), accepted in one action; no unshared session is read | #90 | 2026-11-15 |
| #44 | Cursor source adapter (3.30, Linux): conversations from the state database, prompts of sidebar conversations from transcripts, tag `CU`; work items mentioned only (no exit code recorded) | #92 | 2026-11-15 |
| #39 | Spike: share-link import. Recommendation: do not build (both services' terms forbid programmatic extraction); non-goal updated | #94 | 2026-11-15 |
| #41 | Chats shared by conversation: `sessions` on a project member, `goldengate chats` (list, share, move, unshare), chat projects suggested as named projects | #96 | 2026-11-15 |
| #42 | Incremental re-import of chat exports: compared by conversation id and data, exports that add nothing are not registered, `imports list` shows what each brought in | #98 | 2026-11-15 |
| #23 | Plain words in the UI, help and docs (send, blocked, upload and assistant token, delete from the server, grouping); compatible aliases; TUI goldens refreshed | #100 | 2026-11-16 |
| #46 | Local web server and JSON API over `tui.Backend` on loopback, with the security checks of DESIGN 6.1; `goldengate ui`; contract and security tests | #102 | 2026-11-16 |
| #47 | Web UI v1: Setup, Projects, Preview, Send, Server and Settings in the browser, as plain ES modules on the local API | #104 | 2026-11-16 |
| #48 | Web UI: Inbox, Receipts and Agents in the browser, on the same local API | #106 | 2026-11-16 |

Supporting changes (not queue issues):

| What | PR | Merged |
|---|---|---|
| Go toolchain go1.26.11 (GO-2026-6859 broke `security` on every branch) | #51 | 2026-11-14 |
| First `pr-template` check (unblocked the `main` ruleset); later replaced | #52 | 2026-11-14 |
| The user's stricter `pr-template` check (Checklist with every item ticked; skips Dependabot) | #9 | 2026-11-14 |
| This ledger, then its checklist rule, status section and roadmap sync | #54, #56, #57, #58 | 2026-11-14 |
| Ledger status updates between issues (#49 done; phase 2 complete; refresh; phase 3 complete; #21 done; #35 done; #36 done; #45 done; #31 done; #33 done; #34 done; #32 done; #22 done; #44 done; #39 done; #41 done; #42 done; #23 done; #46 done; #47 done) | #61, #68, #69, #73, #75, #77, #79, #81, #83, #85, #87, #89, #91, #93, #95, #97, #99, #101, #103, #105 | 2026-11-15 |

### Open loose ends

- **User's stash** `list-tools wip (user)` (branch `fix/list-tools-empty-hint`): not touched by the loop. Popping it will conflict with #37 in `listWork` (`bridgemcp/tools.go`).
- **Dependabot #53**: shows a `pr-template` failure from before #9 merged. It clears once the PR is rebased or updated (the user's call).
- **#49 follow-ups**: model summaries pushed before #49 (bullets in `description`) are not counted as stale; they gain a narrative when their session changes or the length setting is changed and the user re-summarizes. (User narratives are stored and edited through #30's summary edits; `summarize.Options.UserNarrative` is no longer the way in.)
- **Threads (#21)**: `internal/utils/threads` groups sessions by shared issues and pull requests. The server's `get_project_context` and `get_work_item` are built on it (#36), from the newest 500 sessions the agent may see; #22 can use it for project suggestions. `get_project_context` no longer returns `unfinished_threads`: it returns `active_threads`. A branch on an issue yields an issue mention with evidence `via: "branch"`, which servers from before #21 reject, so the upgrade order below still applies. Plain numbers in branch names (`feat/21-x`) are deliberately not read as GitHub issues.
- **GitLab (#35)**: provider `gitlab` is new in payloads, so a server from before #35 rejects sessions that carry a GitLab item (upgrade order below). The config is now `version = 3`: a file holding the old default provider list gains `gitlab` once; a list the user changed is left alone. Self-hosted hosts go in `issues.gitlab_hosts` (config file only, no command or TUI setting). The `glab` output shapes in the tests are assumed, not captured from a real run; only URLs and explicit numbers identify items, so a different wording cannot produce a wrong item.
- **Web UI design (#45)**: DESIGN.md 6.1 is the spec for #46 to #48. Decisions made there: no build step and no third-party JavaScript (hand-written HTML, CSS and ES modules, embedded with `embed`); launch token in the URL fragment; a session cookie plus a request-token header on every call; exact Host and Origin checks; write-only secrets. #46 must include the security tests listed at the end of 6.1, and needs web forms of three Backend methods (`DeleteRemote`, `EditBegin`/`EditFinish`, `Init`). The SECURITY.md section is marked planned and must be rewritten as fact when the UI ships.
- **Upgrade order (#19)**: upgrade the server before the client. A server with #19 accepts old clients, but an older server rejects payloads from a newer client (unknown fields). In the server tables the `project` column holds a project id (for data stored before ids, the old key).
- **Per-agent access, as it stands after phase 2**: grants are enforced in every MCP tool (#25). On an upgraded server `Default` keeps the projects it could see at upgrade time and is not granted later ones; on a fresh install `goldengate init` sets `Default` to see every shared project. Not built yet: creating, renaming or removing agents and minting tokens from the TUI (CLI only), and deleting an OAuth application from the client side. "View as agent" is computed on the laptop from what that computer synced, so it does not list sessions synced from another computer. The web UI (#48) should reuse the four agents methods on `tui.Backend`.
- **Decision made by the user for #20 (2026-11-14)**: a project shared from now on waits in the inbox (manual first). Reviewing and pressing Send on the TUI Sync screen is the approval; `goldengate sync`, hooks and timers send only approved sessions. Auto-approve is a per-project switch. The TUI inbox list (`i`) came with #29. Since #31 this is the project's trust level (`trust`: ask, suggest, auto); `auto_approve` is read from old files and not written.
- **One look everywhere (after #48, at the user's request)**: the project's web pages in the personal site are the reference. `docs/brand/BRAND.md`, the web UI's tokens and the terminal's `goldengate-light`/`goldengate-dark` presets carry the same values; `TestGoldenGatePresetsMatchTheWebUI` fails if the terminal and the web UI drift apart. Not redone: `docs/brand/walkthrough-*.gif` and the site's TUI screenshots still show the old terminal colours; the OAuth consent page served by the bridge has its own plain styling. Light-theme Headland and orange sit at 4.2 and 4.3:1 on the TUI cursor row (3.5 before); everything else is AA.
- **Web UI: inbox, agents, receipts (#48)**: `webui/assets/inbox.js`, `receipts.js`, `agents.js`, on the endpoints #46 already served and the contract test that covers them. Walked by hand in a real browser against a local `goldengatebridge`: approve, exclude, edit and save a summary, a receipt from a rule at trust auto, retract from the receipt, a wrong and a right admin secret, grant through the grid with review and apply, view as agent, revoke a token, forget the secret. Not built, because `tui.Backend` does not offer it to any front end: moving a session to another project, adding, renaming or removing an agent, creating an assistant token, changing trust and rules. Fixed in passing: `goldengate init` named the assistant token's rotation by its old command (`token rotate read`).
- **Web UI v1 (#47)**: screens in `webui/assets` (one ES module each): Setup, Projects, Preview, Send, Server, Settings. There is no JavaScript test runner in the repository or CI: Go tests check that the page is self-contained and every imported script is served, and the screens were walked by hand in a real browser against a local `goldengatebridge` (connect, share two projects, preview, review, send, server view, settings, folder view, phone width). Syntax was checked locally with `node --check`, which CI does not run. #48 adds inbox, agents and receipts on the same `api.js` and `dom.js` helpers; `Inbox.Offers`, `Backend.Receipts` and the agents endpoints are already served. Not built: multi-select, renaming a project, the paranoid preview, terminal-style shortcuts.
- **Web API (#46)**: `internal/goldengate/webui` (server, checks, one endpoint per Backend method, sync events over SSE) and `goldengate ui`. The page it serves is a placeholder that proves the session and reads the snapshot; #47 builds the screens on `assets/app.js`'s `api()` helper. Test helpers: `webui/webuitest.Client` (a test browser) and `tui.NewFake` (the TUI's fake, exported for tests through `export_test.go`). Not built from DESIGN 6.1: hashed asset paths with immutable caching, the optional request log. Tried once in a real browser with an empty config (launch, session, snapshot, no console errors); otherwise HTTP-level tests only.
- **Plain words (#23)**: the mapping is in DESIGN.md 1.2 ("Words people read"). Not covered: `goldengatebridge`'s own help and the server API keep `read`/`ingest` scope names; DESIGN.md itself still uses the internal names in technical sections; Go identifiers and config keys are unchanged. New user-facing text should use the plain words: the guard test checks command help only, not TUI strings or messages printed at run time.
- **Per-conversation chats (#41)**: `Member.Sessions` in the config limits a chat grouping to listed conversations; `Syncer.Shared` and `projectOf` honour it; `goldengate chats` lists, shares, moves and unshares; chat projects are suggested through the #22 offer mechanism. Not built: choosing single unassigned conversations in the TUI or the web UI (#48 could add a picker over `app.chats`), and the project list still shows a grouping shared by conversation with its full conversation count. #42 (incremental re-import) can build on `export.Adapter.Heads`.
- **Share-link import (#39, #40)**: the spike recommends not building it: both services' terms forbid programmatic extraction and neither exempts a page the user created (DESIGN.md section 4, "Share-link import: spike result"). #40 is therefore not to be started; closing it as not planned is the user's call. #43 (browser extension spike, stretch) listed #40 as its dependency and would need the same terms question answered first. Whether share pages are server-rendered, and which fields they carry, was deliberately not tested.
- **Cursor adapter (#44)**: built against Cursor 3.30 on Linux from the user's own dummy conversations (read-only chat and search sessions). Source `cursor` is new in payloads and the config is `version = 4` (the old default source list gains `cursor` once), so the upgrade order below applies again. Open, each needing a real conversation to verify: a failing shell command (Cursor stores no exit code, so shell results are not evidence and Cursor work items are `mentioned` only), a file edit (edits are not mapped, `FilesTouched` is empty), a commit or pull request made from Cursor, the `cursor-agent` CLI's store, and the macOS and Windows data locations. A follow-up issue should cover these once such conversations exist.
- **Project suggestions (#22)**: `cli/suggest.go`, on the shared offer mechanism of #32 (`offer`, `allOffers`, `decideOffer`; the TUI and later the web UI see both kinds in `Inbox.Offers`). The unit is an unshared folder, not a single session, and no unshared session is read, in line with the user's #31 decision. Not built: the issue's third signal (chat title or content mentions a project, model only), suggestions for chat groupings, and matching work-item keys found inside unshared sessions. Remote and branch signals need `enrich.git = true`, which has no command or TUI setting (config file only).
- **Rule offers (#32)**: computed in `cli/offers.go` from sent sessions minus rule-sent ones (threshold 5 from one folder or chat grouping within 30 days), shown in `goldengate inbox`, `rules offers|accept|dismiss` and the TUI inbox (`o`); `tui.Inbox.Offers` and `Backend.OfferDecide` are ready for the web UI (#48). Only one kind of offer exists: approve by folder. Sessions sent before receipts existed, or by a project that used to be automatic, count as sent by hand, so such a project may get an offer after being switched to ask.
- **Background sync (#34)**: `goldengate sync --auto` (`RunOpts.Unattended`) plus `install-schedule`, `install-hook claude-code` and their uninstall commands. The installers were tested with injected commands and temporary directories only: no timer or hook was installed on a real system, so `systemctl --user`, `launchctl load` and `schtasks` are unverified in practice, as is reading the token from the OS keyring inside a systemd user service or launchd agent (if the keyring is not reachable there, the run fails with an error in the journal or log). The Claude Code hook uses the documented `hooks.SessionEnd` settings shape and was not tried with a live Claude Code.
- **Receipts (#33)**: one receipt per sync that sent something automatically, written by `sync.Push` from the plan's marks (state tables `receipts`, `receipt_items`); `goldengate receipts`, TUI `c` in the inbox, `tui.Backend.Receipts`. #34's `sync --auto` gets its receipt for free: any sync writes one. The web UI (#48) should reuse `Backend.Receipts` plus `InboxDecide` with retract. The desktop notification (`[ui] notify_receipts`) uses `notify-send` on Linux and `osascript` on macOS and was not tried on a real desktop; Windows has none.
- **Rules and trust (#31)**: `[[projects.rules]]` and `trust` in the config; `goldengate rules`, `projects trust`, `inbox approve --suggested`. `Report.Auto` lists every session sent by trust and rules with its rule id: #33 writes receipts from it, and #34's `sync --auto` needs no new gate (a plain sync already sends only approved or rule-approved sessions). With no approve rule, the trust level covers the whole project. At auto, a grouping reached only through a broader member waits until something was sent from it (`State.KnownGrouping`). Not built: trust and rules in the TUI (the inbox shows the rule note; changing trust or rules is CLI only) and in the web UI (#48); #32 offers rules.
- **Decision made by the user for #31 (2026-11-15)**: a rule acts only on sessions inside folders and chats the user already shares. It belongs to a project and approves or excludes that project's sessions; folders outside projects are still never read. Placing unassigned sessions stays a suggestion the user accepts (#22).
- **Decisions made by the user for #26 (2026-11-14)**: (1) only the admin secret may manage agents, grants and agent tokens; the TUI (#28) should ask for it once per run and keep it in memory only. (2) On a server with no agents yet, `goldengate init` makes the first assistant (`Default`) see every shared project through the per-agent `all_projects` switch; agents added later start with nothing.

## The loop

Work one issue at a time, in queue order. For each issue:

1. **Sync main.**
   ```bash
   git switch main && git pull --ff-only origin main
   ```
2. **Pick the next issue:** the first row in the queue whose status is not `done`. If its "after" issues are not all `done`, stop and report rather than skipping ahead (unless the row is marked as safe to reorder).
3. **Branch** from the fresh main: `<type>/<issue>-<slug>`, e.g. `feat/37-five-day-window`, `docs/17-product-principles`. Types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`.
4. **Update this ledger first:** in the branch's first commit, set the previous issue's row to `done` (with its PR) if it is not already, and this issue's row to `in progress`.
5. **Implement** the issue against its "Done when" list. Keep DESIGN.md, README.md and SECURITY.md in step with behaviour changes.
6. **Verify locally:** `make check` (vet, race tests, staticcheck, golangci-lint) and `govulncheck ./...`. Update golden files only when the change is intended (`go test ./... -update` where a package supports it), and review the diff.
7. **Commit** in Conventional Commit style (`feat(server): ...`) with `Closes #<n>` in the body. **No `Co-Authored-By` trailers.** Stage only the files this issue touched.
8. **Push and open a PR** against `main` using `.github/pull_request_template.md`, in the `Product v2` milestone, with `Closes #<n>` in the summary. End the PR body with the line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
   The `pr-template` check fails unless the description has **Summary**, **Changes**, **Testing** and **Checklist** sections, Summary and Changes are filled in, and **every checklist item is ticked**. Tick an item only when it is true; for an item that does not apply, tick it and add "(n/a: reason)", never leave it unticked. Editing the description re-runs the check.
9. **Wait for CI** to finish (`gh pr checks <pr> --watch`). All checks must pass: `pr-template` (required by the `main` ruleset; skipped for Dependabot), `lint`, `security`, `test-goldengate`, `test-goldengatebridge`, `test-shared`, `test-e2e`.
   - A failure caused by the change: fix it on the same branch and push again.
   - A failure that also fails on `main` (not caused by the change): fix it in its own small PR first (`chore/...`), merge that, rebase this branch on main, and continue. Record it in the log below.
10. **Merge** with a merge commit and delete the branch:
    ```bash
    gh pr merge <pr> --merge --delete-branch
    ```
    If `main` moved since the branch was cut, run `gh pr update-branch <pr>` and wait for CI again. If the ruleset blocks a merge while every check is green, find out which rule (`gh api repos/divyanshusingh2903/goldengate/rules/branches/main`) and fix the cause. Never merge with `--admin` without the user's explicit OK.
11. **Sync main again** (step 1), confirm the issue closed, update the Status section (progress, done table, next up), add a log line below, update the roadmap page, and go back to step 2. Status changes ride on the next issue branch's first commit, with step 4.

### Updating the roadmap page

The page reads all progress from one JSON block near its top, `<script id="status" type="application/json">`. Nothing else on the page needs editing when work moves.

1. Read it with the Artifact tool: `action: "read"`, `url: https://claude.ai/artifact/8LFj2xzfz1Vm6vKamiBS3p`. The result names a saved local file holding the full page.
2. In that file's status JSON, set `"updated"` to today, `"next"` to the next issue number, and add or change the issue's entry: `"<n>": {"state": "done", "pr": <pr>, "merged": "YYYY-MM-DD"}`. States: `done`, `in-progress`, `in-review`, `blocked`; issues not listed show as planned. Add PRs that are not queue issues to `"supporting"`.
3. Publish that file with the Artifact tool and the same `url`, so the link stays the same. Keep the JSON valid.

If no local copy exists (scratch folders do not survive), fetch the page file with `action: "read"`, `path: "index.html"`, and before publishing strip the document skeleton the service added: everything before `<title>` and the closing `</body></html>`.

### Rules that hold across sessions

- Product principles in DESIGN.md 1.1 win over anything an issue body implies. If they conflict, stop and ask the user.
- Never commit, stash-pop, or discard the user's own work in progress. Check `git stash list` at the start of a session: an entry named `list-tools wip (user)` holds the user's uncommitted list-tools fix (`docs/brand/BRAND.md`, `bridgemcp/tools.go`, `e2e/e2e_test.go`, from branch `fix/list-tools-empty-hint`). Leave it for the user.
- Never commit real session data, secrets, or home paths (the pre-commit hook in `.githooks` checks; enable it with `git config core.hooksPath .githooks`).
- One issue per branch and PR. If an issue turns out to need splitting, file the new issue in the milestone, add it to the queue, and note it in the log.
- If an issue cannot be finished (blocked on a decision, an external fact, or a failing dependency), set its row to `blocked`, write why in the log, and stop for the user. Do not start the next issue around it unless it does not depend on the blocked one.

## Queue

Status values: `todo`, `in progress`, `in review`, `blocked`, `done`. Size: S about half a day, M about a day, L a day or more. Planned dates are from the roadmap; the order matters more than the dates.

| # | Issue | Size | After | Planned | Status | PR |
|---|---|---|---|---|---|---|
| 1 | #17 Write down the product principles | S | | Nov 13 | done | #50 |
| 2 | #37 Default 5-day window, custom time ranges | S | | Nov 13 | done | #55 |
| 3 | #18 Named projects with members (client) | L | #17 | Nov 14 | done | #59 |
| 4 | #49 Summaries: facts plus narrative | M | | Nov 15 | done | #60 |
| 5 | #19 Server: stable project IDs, migration | M | #18 | Nov 16 | done | #62 |
| 6 | #24 Server: agents and grants model | M | #19 | Nov 16 | done | #63 |
| 7 | #25 Enforce grants in every MCP tool | M | #24 | Nov 17 | done | #64 |
| 8 | #26 API and CLI for agents and grants | M | #24 | Nov 17 | done | #65 |
| 9 | #27 OAuth consent picks agent and projects | M | #24, #25 | Nov 18 | done | #66 |
| 10 | #28 TUI Agents screen, view as agent | M | #26 | Nov 18 | done | #67 |
| 11 | #20 Local inbox: pending, approved, excluded | L | #17 | Nov 19 | done | #70 |
| 12 | #29 Session include, exclude, retract | S | #20 | Nov 20 | done | #71 |
| 13 | #30 Editable summaries that stay edited | M | #20 | Nov 20 | done | #72 |
| 14 | #21 Threads from work items and branch names | M | | Nov 21 | done | #74 |
| 15 | #35 GitLab issues and merge requests | S | | Nov 21 | done | #76 |
| 16 | #36 MCP context built around work items | M | #21 | Nov 22 | done | #78 |
| 17 | #45 Design and security model for `goldengate ui` | S | #17 | Nov 22 | done | #80 |
| 18 | #31 Rules and per-project trust levels | L | #20, #18 | Nov 23 | done | #82 |
| 19 | #33 Receipts for automatic sends | S | #31, #29 | Nov 24 | done | #84 |
| 20 | #34 Background sync, rule-approved only | M | #31, #33 | Nov 24 | done | #86 |
| 21 | #32 Suggest rules from manual choices | S | #31 | Nov 25 | done | #88 |
| 22 | #22 Suggest projects (inbox only) | M | #18, #20, #21 | Nov 25 | done | #90 |
| 23 | #44 Cursor adapter: spike, then build | L | | Nov 26 | done | #92 |
| 24 | #39 Spike: share-link import | S | #17 | Nov 27 | done | #94 |
| 25 | #41 Per-conversation sharing for chats | M | #18, #29 | Nov 27 | done | #96 |
| 26 | #42 Incremental export re-import | S | #20 | Nov 27 | done | #98 |
| 27 | #40 Share-link import | M | #39, #20 | Nov 28 | blocked | |
| 28 | #23 Plain words in UI and docs | S | #17 | Nov 28 | done | #100 |
| 29 | #46 HTTP JSON API over `tui.Backend` | M | #45 | Nov 29 | done | #102 |
| 30 | #47 Web UI v1: the TUI flow in a browser | L | #46 | Nov 29–30 | done | #104 |
| 31 | #48 Web UI: inbox, agents, receipts | M | #47, #20, #26, #33 | Nov 30 | done | #106 |
| — | #38 Tracker lookups, opt-in per tracker (stretch) | M | #21 | after Nov 30 | todo | |
| — | #43 Spike: browser extension (stretch) | S | #40 | after Nov 30 | todo | |

**Slip list** (move these past Nov 30 first if behind, in this order): #48, #40, #32, #22, #28. Rows 1–10 never slip: they change the payload and the server schema.

**Safe to reorder:** rows with an empty "After" column (#37, #49, #21, #35, #44) can move earlier if a session is blocked on something else.

### Bug fixes from end-to-end testing

Found on 2026-11-17 by driving the TUI, the web UI and the CLI against a local server with synthetic sessions (report: `docs/TEST-REPORT.md`). One branch and PR each, in this order; none depends on another.

| # | Issue | Size | After | Planned | Status | PR |
|---|---|---|---|---|---|---|
| B1 | #112 Redaction: a random-looking folder splits a path | S | | Nov 17 | done | #125 |
| B2 | #113 Moving a partly shared folder into a project re-enables its tools | S | | Nov 17 | done | #126 |
| B3 | #114 CLI share of a parent leaves child projects tombstoned | S | | Nov 17 | done | #127 |
| B4 | #115 MCP recent_activity text under the wrong project | S | | Nov 17 | done | #128 |
| B5 | #116 Projects screen counts excluded sessions as to send | S | | Nov 17 | done | #129 |
| B6 | #117 TUI setup: Esc from a preview leaves setup | S | | Nov 17 | done | #130 |
| B7 | #118 TUI: no-keyring warning printed over the interface | S | | Nov 17 | done | #131 |
| B8 | #119 Held edit conflicts: sync silent, TUI approve claims they go | S | | Nov 17 | done | #132 |
| B9 | #120 TUI view-as: colour codes cut mid-sequence | S | | Nov 17 | done | #133 |
| B10 | #121 TUI search matches the hidden home directory | S | | Nov 17 | done | #134 |
| B11 | #122 Web UI warnings: home path, overflow at phone width | S | | Nov 17 | done | #135 |
| B12 | #123 Web UI settings: wrong API key store label | S | | Nov 17 | done | #136 |
| B13 | #124 UI words: ingest/read token, "sent" for shared, hook text | S | | Nov 17 | done | #137 |

### Web UI restyle

The web UI takes the finalized app design (sidebar, page heads, framed cards, drawer, stepper) in the Golden Gate look. No behaviour or API change.

| # | Issue | Size | After | Planned | Status | PR |
|---|---|---|---|---|---|---|
| R1 | #139 Restyle the web UI to the finalized app design | L | #48 | Nov 18 | done | #141 |
| R2 | #140 State database: concurrent first open fails with SQLITE_BUSY | S | | Nov 18 | done | #142 |
| R3 | #143 Web UI: Receipts leaves the inbox and send counts stale | S | | Nov 18 | done | #144 |
| R4 | #146 Web UI: Projects uses the width; tool logos in place of letter codes | M | #139 | Nov 18 | done | #147 |
| R5 | #148 Web UI: a review you can review, a readable Preview, real review progress | L | #146 | Nov 18 | in review | |

## Log

Newest last. One line per event: date, what happened, PR or issue.

- 2026-11-13: Product v2 issues filed (#11–#49), roadmap set.
- 2026-11-14: #17 opened as #50. CI `security` failed on every branch from GO-2026-6859 in go1.26.10; fixed separately in #51 (toolchain go1.26.11).
- 2026-11-14: User's uncommitted list-tools work stashed as `list-tools wip (user)` so branches can switch cleanly. It edits `listWork` in `bridgemcp/tools.go`, which #37 also changes: expect a conflict there when the user pops it.
- 2026-11-14: Every PR was blocked because the `main` ruleset requires a `pr-template` check that no workflow produced. Added `.github/workflows/pr-template.yml` in #52 (merged first; its only red check was the pre-existing `security` failure).
- 2026-11-14: Merged #52 (pr-template check), #51 (go1.26.11), then #50: #17 done.
- 2026-11-14: Merged #54 (this ledger). #37 implemented: 5-day default, since/until on every time-bounded tool. The per-agent maximum window waits for #24.
- 2026-11-14: #37 done (#55). Replaced the pr-template check from #52 with the user's stricter version from #9 (merged): adds the Checklist section, requires every item ticked, skips Dependabot PRs.
- 2026-11-14: Merged #56 (ledger checklist rule). Added the Status section.
- 2026-11-14: Merged #57 (ledger status section). Roadmap page now reads progress from a status JSON block and shows #17 and #37 done; loop step 11 updates it.
- 2026-11-14: Merged #58 (roadmap page kept in step with the ledger). #18 implemented: `[[projects]]` in the config (members are what is shared, one folder per project, `exclude_sources` per member), legacy `allow`/`deny_source` read as projects, `goldengate projects` commands, TUI `n`/`N`. Server project ids stay as they are until #19.
- 2026-11-14: #18 done (#59). #49 implemented: summaries split into facts (always the local rules) and a narrative (model passages with short/standard/detailed lengths, or the user's); separate narrative caps on client and server; list MCP tools return an abstract and only `get_session` the full narrative.
- 2026-11-14: #49 done (#60). Roadmap page updated. Next: #19.
- 2026-11-14: #19 implemented: `projects` table and project ids on the server, payload version 2 with `project_id`/`project_name`, in-place SQLite upgrade, delete by id, folder or name, MCP tools take a name or id and show the name. The client re-sends stored payloads when a project is renamed or merged, without reading or summarizing again.
- 2026-11-14: #19 done (#62). #24 implemented: `agents` and `agent_grants` tables, every read token and OAuth client belongs to one agent, upgrade gives existing readers a `Default` agent granted every existing project, new projects are granted to no one, grants follow a project to a new id.
- 2026-11-14: #24 done (#63). #25 implemented: the MCP middleware resolves the token's agent and its grants per request; the store applies the scope inside its read queries; an ungranted project looks like a missing one; `max_days` caps every tool (closes the #37 follow-up); the audit log records the agent id.
- 2026-11-14: #25 done (#64). #26 implemented: admin-only agents API (list, create, update, delete, grant, revoke, mint a read token for an agent), `goldengate agents` commands, a per-agent `all_projects` switch, and init turning it on for the first assistant on a server without agents. `token rotate read` now leaves other agents' tokens alone.
- 2026-11-14: #26 done (#65). #27 implemented: OAuth consent is two steps (admin secret, then agent and projects with nothing ticked); the first page shows no agent or project names; the client is attached to the chosen agent.
- 2026-11-14: #27 done (#66). #28 implemented: TUI Agents screen (`5`) with a projects by agents grid, review before apply, view as agent (computed locally), agent details with token revoke; four new `tui.Backend` methods. Not included: creating, renaming or removing agents and minting tokens from the TUI (CLI only for now), and deleting OAuth applications.
- 2026-11-14: #28 done (#67). Phase 2 (per-agent access) is complete: #19, #24, #25, #26, #27, #28. Roadmap page updated. Next: #20.
- 2026-11-14: Ledger refreshed after phase 2 (#69): supporting PRs #61 and #68 listed, loose ends rewritten for the state after #25 to #28. Roadmap page republished.
- 2026-11-14: #20 implemented: per-session decisions in the state DB, `auto_approve` per project, sync sends only approved sessions for projects under review, `goldengate inbox`, `sync --approve`, `projects auto`, `share add --auto-approve`; configurations from before the inbox keep auto-approve on. Existing tests that share and then sync now share with `--auto-approve`.
- 2026-11-14: #20 done (#70). #29 implemented: `goldengate sessions list|include|exclude|retract`, a TUI inbox screen (`i`) with approve, exclude, undo and retract, retract deleting the session on the server before recording it locally. Also fixed in this PR: a session waiting for approval was summarized by the model during a plain sync; it is now built with the local rules until approved, and listings never call a model.
- 2026-11-14: #29 done (#71). #30 implemented: `goldengate sessions edit` and `e` in the TUI inbox open the summary in the user's editor; the edit is stored per session and content hash, used instead of any summarizer, redacted, and never overwritten; a session that changes after an edit is held until the user edits again, keeps or discards the edit.
- 2026-11-14: #30 done (#72). Phase 3 (inbox and curation) is complete: #20, #29, #30. Roadmap page updated. Next: #21.
- 2026-11-14: #21 implemented: issue keys are read from branch names with the known-prefix rule (evidence `branch`, status `mentioned`), and `internal/utils/threads` groups sessions across sources into threads by shared issues and pull requests, joining an issue to a pull request only through the branch or the pull request title. Golden test in `testdata/threads`.
- 2026-11-14: #21 done (#74). Roadmap page updated. Next: #35.
- 2026-11-14: #35 implemented: GitLab issue and merge request URLs (gitlab.com and hosts in `issues.gitlab_hosts`), `glab mr` and `glab issue` results as evidence, provider `gitlab` with merge requests as `pull_request`; threads match short GitLab keys like GitHub ones. Config version 3.
- 2026-11-14: #35 done (#76). Roadmap page updated. Next: #36.
- 2026-11-15: #36 implemented: `get_project_context` leads with active threads (item, status as of the session, short summary, tools, latest session); new MCP tool `get_work_item` returns one thread with linked sessions across sources and the status history with evidence; tool descriptions say when to use each.
- 2026-11-15: #36 done (#78). Roadmap page updated. Next: #45.
- 2026-11-15: #45 written: DESIGN.md 6.1 (process and lifetime, launch token and session, request checks, write-only secrets, untrusted text, API shape, frontend stack and embedding) with a review against SECURITY.md, and a planned-feature section in SECURITY.md. No code.
- 2026-11-15: #45 done (#80). Roadmap page updated. Next: #31.
- 2026-11-15: #31 implemented: per-project trust levels (ask, suggest, auto) and user-written rules on deterministic facts (source, folder or chat grouping, remote, branch, work-item prefix) that approve or exclude sessions inside a project; exclude wins; a grouping reached only through a broader member is asked about first; property tests for deny beats rules, a rule never matches outside its pattern, and the new-match confirmation. User decision recorded: rules act only inside what is shared.
- 2026-11-15: #31 done (#82). Roadmap page updated. Next: #33.
- 2026-11-15: #33 implemented: every automatic send is written into exactly one receipt with the rule that sent it; `goldengate receipts` (list, show with `--agents`, retract), a TUI receipts screen, retractions marked in receipts, and an opt-in desktop notification with counts only.
- 2026-11-15: #33 done (#84). Roadmap page updated. Next: #34.
- 2026-11-15: #34 implemented: `goldengate sync --auto` for unattended runs (only approved or rule-approved sessions, one-line report, everything sent listed in the receipt), `install-schedule` (systemd user timer, launchd agent, Task Scheduler) and `install-hook claude-code`, each with `--print` and an uninstall command.
- 2026-11-15: #34 done (#86). Roadmap page updated. Next: #32.
- 2026-11-15: #32 implemented: after 5 sessions sent by hand from one folder or chat grouping within 30 days, an offer shows the exact rule, what it would have matched and what accepting changes; only a yes adds it; not now is remembered; shown in the CLI inbox, `rules offers` and the TUI inbox.
- 2026-11-15: #32 done (#88). Roadmap page updated. Next: #22.
- 2026-11-15: #22 implemented: a folder with sessions that is in no project gets a suggested project and a reason in the inbox (worktree of a member; with enrich.git, same remote or a branch naming one of the project's issues), accepted in one action or turned down for good, without reading any unshared session.
- 2026-11-15: #22 done (#90). Roadmap page updated. Next: #44.
- 2026-11-15: #44 spike and build: Cursor was not installed; the user installed 3.30 and made dummy conversations. Found: conversations in `globalStorage/state.vscdb` (`composerHeaders`, `bubbleId:*` rows), prompts of sidebar conversations only in `~/.cursor/projects/*/agent-transcripts`, no exit code for shell calls. Built a read-only adapter (tag `CU`), synthetic SQL fixtures in `testdata/cursor/3.30`, golden tests.
- 2026-11-15: #44 done (#92). Roadmap page updated. Next: #39.
- 2026-11-15: #39 spike done: read both services' terms and robots.txt; recommendation is do not build share-link import; DESIGN.md non-goal updated. No code.
- 2026-11-15: #39 done (#94). #40 set to blocked: the spike says do not build; the user decides whether to close it. Roadmap page updated. Next: #41.
- 2026-11-15: #41 implemented: chats are shared by conversation (`sessions` on a project member, `goldengate chats`), chat projects are suggested as named projects, the inbox counts unassigned conversations, and whole-grouping shares keep working unchanged.
- 2026-11-15: #41 done (#96). Roadmap page updated. Next: #42.
- 2026-11-15: #42 implemented: importing an export compares it with the registered ones (by conversation id and data), reports new, changed and already-imported conversations, does not register an export that adds nothing, and records what each export brought in for `goldengate imports list`.
- 2026-11-15: #42 done (#98). Roadmap page updated. Next: #23.
- 2026-11-15: #23 implemented: UI, command help, README and SECURITY.md say send, blocked, upload token, assistant token, delete from the server and grouping; aliases `send`, `share block`, `--delete-from-server`, `--keep-on-server`, `token rotate assistant|upload`; older spellings keep working; TUI goldens refreshed; a test guards command help.
- 2026-11-16: #23 done (#100). Roadmap page updated. Next: #46.
- 2026-11-16: #46 implemented: local web server on loopback with launch token, session cookie plus request token, Host and Origin checks, no CORS; every `tui.Backend` method reachable over a JSON API, sync progress as server-sent events; `goldengate ui`; contract test over the TUI's fake backend and the security tests from DESIGN 6.1.
- 2026-11-16: #46 done (#102). Roadmap page updated. Next: #47.
- 2026-11-16: #47 implemented: the web UI's first screens (Setup, Projects, Preview, Send, Server, Settings) as plain ES modules on the #46 API; tried end to end in a real browser against a local server. A trial upload token landed in the OS keyring during that test and was removed again.
- 2026-11-16: #47 done (#104). Roadmap page updated. Next: #48.
- 2026-11-16: #48 implemented: Inbox, Receipts and Agents in the web UI, tried end to end in a real browser against a local server; each screen visit now draws into its own element. The trial's upload token was removed from the OS keyring again.
- 2026-11-16: #48 done (#106). Roadmap page updated. The queue is finished except #40, which is blocked on the user's decision.
- 2026-11-16: Web UI restyled to the project's web pages (fonts, mark, square double-rule surfaces, neutral dark theme, round state glyphs, short source tags), at the user's request; tried in a browser in light, dark and at phone width.
- 2026-11-16: BRAND.md and the terminal's Golden Gate presets moved to the same values as the site and the web UI, with a test that keeps them together.
- 2026-11-17: End-to-end test of the TUI, web UI and CLI against a local server with synthetic sessions; 13 bugs filed (#112–#124) and queued as bug fixes. #112 in progress.
- 2026-11-17: #112 done (#125): paths are scrubbed before the high-entropy pass. #113 in progress.
- 2026-11-17: #113 done (#126): a shared folder moved into a project keeps its switched-off tools. #114 in progress.
- 2026-11-17: #114 done (#127): `share add` and `projects add` lift the tombstones of every project they start sharing. #115 in progress.
- 2026-11-17: #115 done (#128): recent_activity's text lists each session under its own project. #116 in progress.
- 2026-11-17: #116 done (#129): excluded and retracted sessions are not counted as to send. #117 in progress.
- 2026-11-17: #117 done (#130): Esc from a preview opened in setup goes back to setup. #118 in progress.
- 2026-11-17: #118 done (#131): the no-keyring warning is a notice in the TUI and web UI instead of a line over the screen. #119 in progress.
- 2026-11-17: #119 done (#132): sync says when edited sessions are held; the TUI no longer approves them.
- 2026-11-17: #120 done (#133): view-as lines are cut by visible width.
- 2026-11-17: #121 done (#134): the TUI search matches what is shown. #122 in progress.
- 2026-11-17: #122 done (#135): web UI warnings show the ~ form and wrap at phone width. #123 in progress.
- 2026-11-17: #123 done (#136): the web UI's Settings say where the summarizer key really is.
- 2026-11-17: #124 done (#137): upload token, assistant token, shared, and the install commands in the TUI. Every bug from the end-to-end test is fixed; report in `docs/TEST-REPORT.md`.
- 2026-11-17: #139 implemented: the web UI takes the finalized app design (sidebar with counts, Overview, page heads and cards, inbox tabs with multi-select and a session drawer, send stepper with progress, stat cards on Server, rows with switches in Settings, a card per agent). Walked in a real browser against a local `goldengatebridge` with synthetic sessions: setup, share, inbox select and approve, drawer edit and Esc cancel, review and send, server, agents (grant staged and discarded, view as), settings, preview, receipts; light and dark; no sideways scroll at 375px. Found #140 (concurrent first open of the state database fails with SQLITE_BUSY).
- 2026-11-17: #140 done (#142): opening the state database serializes creating the schema inside the process and retries SQLITE_BUSY for up to 10 s (another process doing the same); a test opens a new database from eight goroutines at once, 20 rounds. The web UI loads its first calls in parallel again.
- 2026-11-17: Receipts tested with real automatic sends (trust auto on one project, an approve rule on another, `sync --auto`): the receipt, its reasons, the Overview timeline and Retract all work, and a first load on a new state directory succeeds. Found #143: Receipts never refreshes the sidebar counts.
- 2026-11-17: #143 done (#144): Receipts refreshes the sidebar counts and the send button; a test checks every screen that shows or changes what waits. Rechecked every web UI screen on main against the sandbox server. The roadmap page has a Testing and polish section with the 13 test bugs, #139, #140 and #143.
- 2026-11-17: #146 implemented: Projects is a wide screen (up to 1880px) with the list taking 5/11 of it and the chosen project's cards beside it, rows led by the folder name with the parent folder (or "worktree of …") beneath, and long names breaking at hyphens. Tools show their Simple Icons logos (CC0) in the text colour, Claude's in its terracotta, named for screen readers; an unknown tool shows its name. Checked at 1920, 1280 and 375px, light and dark, with nine extra synthetic folders and a worktree.
- 2026-11-17: #148 implemented: the plan carries every session it sends (`PlanInfo.Items`, from `Batch.Items`, with new or changed and the rule's reason), and planning reports each queued session (`RunOpts.OnQueued`, `SyncQueued`) so the review counts n of total. The web review lists the sessions; each opens in a wide drawer as a readable summary (narrative, asked for, done, outcomes, left open, work items, files, tools) with the exact JSON as a folding, coloured tree, redactions marked in both, and Keep local re-runs the review without it. Preview uses the same view. Checked at 1440 and 375px, light and dark, with a planted AWS key and a synthetic model-written summary.
