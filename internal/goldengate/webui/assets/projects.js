// Projects: what is on this computer, what is shared, and what the next
// send would take.

import { api } from "/assets/api.js";
import { h, clear, when, plural, ask, put, fill, tag, head, card, cardBody, note } from "/assets/dom.js";

const NOT = 0, SHARED = 1, PARTIAL = 2, BLOCKED = 3;
const ALLOW = 0, REMOVE = 1, DENY = 2, TOGGLE = 3, SETPROJECT = 4;

const ui = { view: "all", query: "", tree: false, selected: "" };

// initials are the first letters of a folder's last part, for its tile.
function initials(label) {
  const last = String(label || "?").split("/").filter(Boolean).pop() || "?";
  return last.replace(/[^A-Za-z0-9]/g, "").slice(0, 2).toUpperCase() || "?";
}

function isShared(r) { return r.State === SHARED || r.State === PARTIAL; }

function stateText(r) {
  if (r.State === BLOCKED) return ["✕ blocked", "bad"];
  if (!isShared(r)) return ["not shared", "dim"];
  if (r.Pending > 0) return [`▲ ${r.Pending} to send`, "warn"];
  if (r.Synced > 0) return [`✓ sent ${when(r.LastSync)}`, "good"];
  return ["shared", "good"];
}

// box is the state glyph. The terminal UI uses brackets; the web uses the
// round glyphs of docs/brand, always next to a word that says the same.
function box(r) {
  return { [NOT]: ["○", "none"], [SHARED]: ["●", "good"], [PARTIAL]: ["◐", "warn"], [BLOCKED]: ["✕", "bad"] }[r.State];
}

// card says in words what happens to the project.
function cardText(r) {
  const n = plural(r.Sessions, "session");
  if (r.State === BLOCKED) return ["✕ Blocked", "Never sent, even when a parent folder is shared. Share it to lift the block."];
  if (!isShared(r)) return ["Not shared", `Nothing from this project leaves your machine. If you share it, the next send has ${n}, after you review them.`];
  const head = r.State === PARTIAL ? "◐ Partly shared" : "● Shared";
  if (r.Synced === 0 && r.Pending > 0) return [head + " · never sent", `Not on the server yet. The next send has ${plural(r.Pending, "session")}.`];
  if (r.Pending > 0) return [head + ` · ▲ ${r.Pending} to send`, `${plural(r.Pending, "session")} new or changed since it was last sent.`];
  if (r.Synced > 0) return [head + " · up to date", "The server has everything from this project."];
  return [head, "No sessions in the look-back window yet; new ones go out the next time you send."];
}

export async function render(main, app) {
  const snap = await app.refresh();
  const rows = snap.Rows || [];
  if (!rows.some((r) => r.Key === ui.selected)) ui.selected = rows.length ? rows[0].Key : "";

  const act = async (action, done) => {
    try {
      const res = await api("/api/apply", action);
      app.say(res.Message || done);
      if (res.Remote) {
        const yes = await ask("Delete from the server too?",
          [`${plural(res.Remote.Count, "project")} you stopped sharing still ${res.Remote.Count === 1 ? "has" : "have"} data on the server.`,
            "Deleting removes it for every assistant at once. Files on this computer are not touched."], "Delete from the server");
        if (yes) app.say((await api("/api/delete-remote", { ID: res.Remote.ID })).Message);
      }
    } catch (e) {
      app.say(String(e.message || e), true);
    }
    await render(main, app);
  };

  const counts = {
    all: rows.length, shared: rows.filter(isShared).length,
    tosend: rows.filter((r) => isShared(r) && r.Pending > 0).length, notshared: rows.filter((r) => !isShared(r)).length,
  };
  const inView = (r) => ({ all: true, shared: isShared(r), tosend: isShared(r) && r.Pending > 0, notshared: !isShared(r) }[ui.view]);
  const q = ui.query.trim().toLowerCase();
  const shown = rows.filter((r) => inView(r) && (!q || r.Label.toLowerCase().includes(q) || (r.Project || "").toLowerCase().includes(q)));

  const chip = (id, label) => h("button", { type: "button", "aria-pressed": String(ui.view === id), onclick: () => { ui.view = id; render(main, app); } }, `${label} ${counts[id]}`);
  const search = h("input", { type: "search", value: ui.query, placeholder: "Search projects", "aria-label": "Search projects" });
  search.addEventListener("input", () => { ui.query = search.value; drawList(); });

  const list = h("ul", { class: "plist" });
  const detail = h("div", { class: "grid" });

  const rowButton = (r, depth) => {
    const [text, cls] = stateText(r);
    return h("li", null, h("button", {
      type: "button", class: "pitem" + (depth ? ` indent-${Math.min(depth, 4)}` : ""), "aria-current": String(r.Key === ui.selected),
      onclick: () => { ui.selected = r.Key; drawList(); drawDetail(); },
      ondblclick: () => app.go("preview", { key: r.Key, label: r.Label }),
    },
    h("span", { class: "glyph " + box(r)[1], title: ["not shared", "shared", "some tools shared", "blocked"][r.State] }, box(r)[0]),
    h("span", { class: "nm" }, r.Label,
      r.Project && r.Project !== r.Label ? h("small", null, r.Project) : null),
    (r.Sources || []).length ? h("span", { class: "tags" }, (r.Sources || []).map((s) => tag(s.Name))) : null,
    h("span", { class: "state " + cls }, text)));
  };

  function drawList() {
    const q2 = ui.query.trim().toLowerCase();
    const vis = rows.filter((r) => inView(r) && (!q2 || r.Label.toLowerCase().includes(q2) || (r.Project || "").toLowerCase().includes(q2)));
    clear(list);
    if (!vis.length) {
      put(list, h("li", null, h("p", { class: "folder" }, rows.length ? "Nothing matches this view." : "No sessions were found on this computer yet.")));
      return;
    }
    if (!ui.tree) {
      for (const r of vis) put(list, rowButton(r, 0));
      return;
    }
    // tree: folders that hold several projects become a row you can share as a whole
    const folders = new Map();
    for (const r of vis) {
      if (r.Virtual) continue;
      const parts = r.Label.split("/");
      const keyParts = r.Key.split("/");
      for (let i = 1; i < parts.length; i++) {
        const label = parts.slice(0, i).join("/");
        const f = folders.get(label) || { label, key: keyParts.slice(0, keyParts.length - (parts.length - i)).join("/"), n: 0 };
        f.n++;
        folders.set(label, f);
      }
    }
    const shownFolders = [...folders.values()].filter((f) => f.n > 1 && f.label !== "~" && f.label !== "");
    const entries = [...shownFolders.map((f) => ({ sort: f.label, folder: f })), ...vis.map((r) => ({ sort: r.Label, row: r }))];
    entries.sort((a, b) => a.sort.localeCompare(b.sort));
    const depthOf = (label) => shownFolders.filter((f) => label.startsWith(f.label + "/")).length;
    for (const e of entries) {
      if (e.row) { put(list, rowButton(e.row, e.row.Virtual ? 0 : depthOf(e.row.Label))); continue; }
      const f = e.folder;
      put(list, h("li", null, h("div", { class: "folder" + (depthOf(f.label) ? ` indent-${Math.min(depthOf(f.label), 4)}` : "") },
        h("span", null, `▾ ${f.label}/ (${plural(f.n, "project")})`),
        h("button", {
          type: "button", class: "sm",
          onclick: async () => {
            if (await ask(`Share everything under ${f.label}?`, ["Every project in this folder becomes shared, including ones that appear later.", "Their sessions are sent only when you review and press Send."], "Share the folder")) {
              act({ Kind: ALLOW, Keys: [f.key] }, "shared");
            }
          },
        }, "Share this folder"))));
    }
  }

  function drawDetail() {
    clear(detail);
    const r = rows.find((x) => x.Key === ui.selected);
    if (!r) { put(detail, card(null, null, cardBody(h("p", { class: "dim" }, "Select a project.")))); return; }
    const [head2, text] = cardText(r);
    const others = (r.Sources || []).map((s) => s.Name);
    const [, cls] = stateText(r);
    put(detail, card(
      h("div", { class: "title-cell" }, h("span", { class: "avatar", "aria-hidden": "true" }, initials(r.Label)),
        h("div", null, h("h2", null, r.Label), h("small", null, `${plural(r.Sessions, "session")}${when(r.Last) ? ", last active " + when(r.Last) : ""}`))),
      h("span", { class: "badge " + cls }, head2),
      cardBody(
        h("p", null, text),
        h("div", { class: "facts gap-top" },
          h("div", null, h("div", { class: "k" }, "Project"), h("div", { class: "v" }, r.Project || "none")),
          h("div", null, h("div", { class: "k" }, "Tools"), h("div", { class: "v" }, (r.Sources || []).length ? h("span", { class: "tags" }, r.Sources.map((s) => tag(s.Name))) : "none")),
          h("div", null, h("div", { class: "k" }, "Waiting to send"), h("div", { class: "v" }, isShared(r) ? String(r.Pending || 0) : "—"))),
        r.Root ? h("p", { class: "hint gap-top" }, `A worktree of ${r.Root}`) : null,
        r.Reason ? h("p", { class: "hint gap-top" }, r.Reason) : null),
      h("div", { class: "card-foot" },
        h("button", { type: "button", onclick: () => app.go("preview", { key: r.Key, label: r.Label }) }, "Preview what would be sent"),
        r.State !== BLOCKED ? h("button", {
          type: "button", class: "danger",
          onclick: async () => {
            if (await ask(`Block ${r.Label}?`, ["A blocked folder is never sent, even when a parent folder is shared."], "Block")) act({ Kind: DENY, Keys: [r.Key] }, "blocked");
          },
        }, "Block") : null,
        isShared(r)
          ? h("button", { type: "button", onclick: () => act({ Kind: REMOVE, Keys: [r.Key] }, "no longer shared") }, "Stop sharing")
          : h("button", { type: "button", class: "primary", onclick: () => act({ Kind: ALLOW, Keys: [r.Key] }, "shared") }, "Share"))));
    if ((r.Sources || []).length && r.State !== BLOCKED) {
      put(detail, card("Tools", null, h("div", null, r.Sources.map((s) => {
        const cb = h("input", { type: "checkbox", class: "switch", checked: !!s.Shared, disabled: !!s.OffEverywhere, "aria-label": `Share ${s.Name} sessions of this project` });
        cb.addEventListener("change", () => act({ Kind: TOGGLE, Keys: [r.Key], Source: s.Name, Others: others.filter((n) => n !== s.Name) }, "changed"));
        return h("div", { class: "set-row" },
          h("div", null, h("div", { class: "t" }, tag(s.Name), s.Name), h("div", { class: "d" }, plural(s.Sessions, "session"),
            s.OffEverywhere ? h("span", { class: "warn" }, " · off for every project in Settings") : null)),
          cb);
      }))));
    }
    const name = h("input", { type: "text", value: r.Project || "", "aria-label": "Project name", placeholder: "Project name" });
    put(detail, card("Project", null, cardBody(
      h("p", { class: "hint" }, "Put this folder in a named project. Doing so shares it."),
      h("form", { class: "row", onsubmit: (e) => { e.preventDefault(); if (name.value.trim()) act({ Kind: SETPROJECT, Keys: [r.Key], Project: name.value.trim() }, "moved"); } },
        h("span", { class: "grow-field" }, name), h("button", { type: "submit" }, "Put in project")))));
  }

  const treeBox = h("input", { type: "checkbox", checked: ui.tree });
  treeBox.addEventListener("change", () => { ui.tree = treeBox.checked; drawList(); });

  fill(main,
    head("Projects", "What is on this computer, and what is shared.",
      h("button", {
        type: "button",
        onclick: async () => { try { app.say((await api("/api/undo", {})).Message); } catch (e) { app.say(String(e.message || e), true); } render(main, app); },
      }, "Undo")),
    (snap.Warnings || []).map((w) => h("p", { class: "banner warn" }, "⚠ " + w)),
    (snap.Notices || []).map((w) => h("p", { class: "banner warn" }, "Note: " + w)),
    h("div", { class: "toolbar" },
      h("div", { class: "seg", role: "group", "aria-label": "Show" }, chip("all", "All"), chip("shared", "Shared"), chip("tosend", "To send"), chip("notshared", "Not shared")),
      search,
      h("label", { class: "check" }, treeBox, "Show as folders")),
    h("div", { class: "proj" }, h("div", { class: "card" }, list), detail),
    h("p", { class: "legend dim" },
      h("span", null, h("span", { class: "glyph good" }, "●"), " shared"), h("span", null, h("span", { class: "glyph none" }, "○"), " not shared"),
      h("span", null, h("span", { class: "glyph warn" }, "◐"), " some tools shared"), h("span", null, h("span", { class: "glyph bad" }, "✕"), " blocked"),
      h("span", null, "Double-click a project to preview it.")),
    note("Sharing a project selects it; nothing is sent from this screen. Sessions leave this computer only when you review them and press Send."),
  );
  void shown;
  drawList();
  drawDetail();
}
