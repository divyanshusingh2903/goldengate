# Product v2 work ledger

This file lets any Claude Code session (or person) pick up the Product v2 work where the last one stopped. **Read it before starting work, and update it as part of every issue.**

- Plan and reasons: `DESIGN.md` section 1.1 (product principles) and the GitHub [Product v2 milestone](https://github.com/divyanshusingh2903/goldengate/milestone/1).
- Each issue's body is its spec: scope, rules, and "Done when". Read it in full with `gh issue view <n>` before writing code.
- Target: every issue in the queue merged by **2026-11-30**.
- Roadmap page (Claude artifact, private to the owner): https://claude.ai/artifact/8LFj2xzfz1Vm6vKamiBS3p. It shows the same order day by day, with progress. Keep it in step with this file (loop step 11).

## Status

_Last updated 2026-11-14. Update this section whenever a row in the queue changes._

**Progress:** 10 of 31 scheduled issues done (plus 2 stretch). Phases 1 (Foundations) and 2 (Per-agent access) are complete.
**Next up:** #20 Local inbox: pending, approved, excluded, size L, planned Nov 19 (first issue of phase 3). Its dependency, #17, is done.

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

Supporting changes (not queue issues):

| What | PR | Merged |
|---|---|---|
| Go toolchain go1.26.11 (GO-2026-6859 broke `security` on every branch) | #51 | 2026-11-14 |
| First `pr-template` check (unblocked the `main` ruleset); later replaced | #52 | 2026-11-14 |
| The user's stricter `pr-template` check (Checklist with every item ticked; skips Dependabot) | #9 | 2026-11-14 |
| This ledger, then its checklist rule, status section and roadmap sync | #54, #56, #57, #58 | 2026-11-14 |
| Ledger status updates between issues (#49 done; phase 2 complete; this refresh) | #61, #68, #69 | 2026-11-14 |

### Open loose ends

- **User's stash** `list-tools wip (user)` (branch `fix/list-tools-empty-hint`): not touched by the loop. Popping it will conflict with #37 in `listWork` (`bridgemcp/tools.go`).
- **Dependabot #53**: shows a `pr-template` failure from before #9 merged. It clears once the PR is rebased or updated (the user's call).
- **#49 follow-ups**: the user's own narrative is honoured through `summarize.Options.UserNarrative`, but nothing stores or edits one until #30. Model summaries pushed before #49 (bullets in `description`) are not counted as stale; they gain a narrative when their session changes or the length setting is changed and the user re-summarizes. `get_work_item` (#36) should return the full narrative like `get_session`.
- **Upgrade order (#19)**: upgrade the server before the client. A server with #19 accepts old clients, but an older server rejects payloads from a newer client (unknown fields). In the server tables the `project` column holds a project id (for data stored before ids, the old key).
- **Per-agent access, as it stands after phase 2**: grants are enforced in every MCP tool (#25). On an upgraded server `Default` keeps the projects it could see at upgrade time and is not granted later ones; on a fresh install `goldengate init` sets `Default` to see every shared project. Not built yet: creating, renaming or removing agents and minting tokens from the TUI (CLI only), deleting an OAuth application from the client side, and `get_work_item` (#36), which should return the full narrative like `get_session`. "View as agent" is computed on the laptop from what that computer synced, so it does not list sessions synced from another computer. The web UI (#48) should reuse the four agents methods on `tui.Backend`.
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
| 11 | #20 Local inbox: pending, approved, excluded | L | #17 | Nov 19 | todo | |
| 12 | #29 Session include, exclude, retract | S | #20 | Nov 20 | todo | |
| 13 | #30 Editable summaries that stay edited | M | #20 | Nov 20 | todo | |
| 14 | #21 Threads from work items and branch names | M | | Nov 21 | todo | |
| 15 | #35 GitLab issues and merge requests | S | | Nov 21 | todo | |
| 16 | #36 MCP context built around work items | M | #21 | Nov 22 | todo | |
| 17 | #45 Design and security model for `goldengate ui` | S | #17 | Nov 22 | todo | |
| 18 | #31 Rules and per-project trust levels | L | #20, #18 | Nov 23 | todo | |
| 19 | #33 Receipts for automatic sends | S | #31, #29 | Nov 24 | todo | |
| 20 | #34 Background sync, rule-approved only | M | #31, #33 | Nov 24 | todo | |
| 21 | #32 Suggest rules from manual choices | S | #31 | Nov 25 | todo | |
| 22 | #22 Suggest projects (inbox only) | M | #18, #20, #21 | Nov 25 | todo | |
| 23 | #44 Cursor adapter: spike, then build | L | | Nov 26 | todo | |
| 24 | #39 Spike: share-link import | S | #17 | Nov 27 | todo | |
| 25 | #41 Per-conversation sharing for chats | M | #18, #29 | Nov 27 | todo | |
| 26 | #42 Incremental export re-import | S | #20 | Nov 27 | todo | |
| 27 | #40 Share-link import | M | #39, #20 | Nov 28 | todo | |
| 28 | #23 Plain words in UI and docs | S | #17 | Nov 28 | todo | |
| 29 | #46 HTTP JSON API over `tui.Backend` | M | #45 | Nov 29 | todo | |
| 30 | #47 Web UI v1: the TUI flow in a browser | L | #46 | Nov 29–30 | todo | |
| 31 | #48 Web UI: inbox, agents, receipts | M | #47, #20, #26, #33 | Nov 30 | todo | |
| — | #38 Tracker lookups, opt-in per tracker (stretch) | M | #21 | after Nov 30 | todo | |
| — | #43 Spike: browser extension (stretch) | S | #40 | after Nov 30 | todo | |

**Slip list** (move these past Nov 30 first if behind, in this order): #48, #40, #32, #22, #28. Rows 1–10 never slip: they change the payload and the server schema.

**Safe to reorder:** rows with an empty "After" column (#37, #49, #21, #35, #44) can move earlier if a session is blocked on something else.

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
