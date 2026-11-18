// Agents: which assistant sees which project. Every call here needs the
// admin secret, which the local server keeps in memory for this run only.

import { api } from "/assets/api.js";
import { h, fill, put, when, plural, ask, tag, head, card, cardBody, note, empty, table, drawer } from "/assets/dom.js";

let pending = []; // grant changes chosen but not applied yet

export async function render(main, app) {
  let info;
  try {
    info = await api("/api/agents");
  } catch (e) {
    if (e.kind === "admin_required") { askSecret(main, app); return; }
    throw e;
  }
  const agents = info.Agents || [];
  const projects = info.Projects || [];
  const key = (c) => `${c.AgentID}|${c.All ? "*" : c.ProjectID}`;
  const staged = (c) => pending.find((p) => key(p) === key(c));
  const toggle = (c, now) => {
    const have = staged(c);
    if (have) pending = pending.filter((p) => p !== have); // back to what the server has
    else pending.push({ ...c, Grant: !now });
    render(main, app);
  };
  const cell = (a, p) => {
    const c = { AgentID: a.ID, AgentName: a.Name, ProjectID: p ? p.ID : "", ProjectName: p ? p.Name : "", All: !p };
    const now = p ? !!(a.Grants || {})[p.ID] : !!a.AllProjects;
    const s = staged(c);
    const shown = s ? s.Grant : now;
    const viaAll = p && !shown && a.AllProjects && !staged({ AgentID: a.ID, All: true });
    const box = h("input", { type: "checkbox", checked: shown || viaAll, disabled: viaAll, "aria-label": `${a.Name} sees ${p ? p.Name : "every project"}` });
    box.addEventListener("change", () => toggle(c, now));
    return h("td", { class: s ? "changed" : "" }, box, s ? h("span", { class: "warn" }, s.Grant ? " + to grant" : " − to revoke") : viaAll ? h("span", { class: "dim" }, " (every project)") : null);
  };

  const forget = h("button", {
    type: "button", class: "ghost",
    onclick: async () => { await api("/api/admin/forget", {}); pending = []; app.say("The admin secret was forgotten."); render(main, app); },
  }, "Forget the admin secret");
  fill(main,
    head("Agents", "Each assistant sees only the projects you grant it.", forget),
    card("Who can read what", null,
      h("div", { class: "tbl" }, h("table", { class: "matrix" },
        h("thead", null, h("tr", null, h("th", null, "Project"), agents.map((a) => h("th", null, a.Name)))),
        h("tbody", null,
          h("tr", null, h("td", null, h("strong", null, "Every project, including future ones")), agents.map((a) => cell(a, null))),
          projects.map((p) => h("tr", null, h("td", null, p.Name, h("small", null, plural(p.Sessions, "session"))), agents.map((a) => cell(a, p))))))),
      pending.length ? null : h("div", { class: "card-foot" }, h("span", { class: "grow hint" }, "Ticking a box changes nothing until you review and apply."))));

  if (pending.length) {
    put(main, h("div", { class: "bulk" },
      h("span", null, `${plural(pending.length, "change")} to review`),
      h("button", { type: "button", class: "sm", onclick: () => { pending = []; render(main, app); } }, "Discard"),
      h("button", {
        type: "button", class: "sm primary",
        onclick: async () => {
          const lines = pending.map((c) => c.Grant
            ? `${c.AgentName} will see ${c.All ? "every project, including future ones" : c.ProjectName}`
            : `${c.AgentName} will no longer see ${c.All ? "every project (only the ones ticked for it)" : c.ProjectName}`);
          if (!await ask(`Apply ${plural(pending.length, "change")}?`, lines, "Apply")) return;
          try {
            await api("/api/agents/grants", { Changes: pending });
            app.say(`applied ${plural(pending.length, "change")}`);
            pending = [];
          } catch (e) {
            app.say(String(e.message || e) + " Changes before the failure were applied.", true);
            pending = [];
          }
          render(main, app);
        },
      }, "Review and apply")));
  }

  put(main, h("h2", { class: "section-title" }, "Each agent"),
    agents.length ? h("div", { class: "grid g3" }, agents.map((a) => agentCard(main, app, a))) : h("div", { class: "card" }, empty(null, null, "No agents yet.")),
    note("Agents only read. New projects are visible to no agent until you grant them."));
}

function agentCard(main, app, a) {
  const tokens = a.Tokens || [];
  return h("section", { class: "card agent" },
    h("div", { class: "card-body" },
      h("div", { class: "agent-top" }, h("span", { class: "avatar", "aria-hidden": "true" }, (a.Name || "?").slice(0, 1).toUpperCase()),
        h("div", null, h("h3", null, a.Name), h("small", { class: "faint" }, when(a.LastUsed) ? `last used ${when(a.LastUsed)}` : "never used")),
        h("span", { class: "badge " + (tokens.length || (a.Clients || []).length ? "good" : "dim") }, tokens.length || (a.Clients || []).length ? "● can read" : "○ no token")),
      h("dl", { class: "kv" },
        h("dt", null, "Sees"), h("dd", null, a.AllProjects ? "every project" : plural(Object.keys(a.Grants || {}).length, "project")),
        h("dt", null, "Window"), h("dd", null, a.MaxDays ? `the last ${a.MaxDays} days` : "no limit"),
        (a.Clients || []).length ? [h("dt", null, "Apps"), h("dd", null, a.Clients.join(", "))] : null),
      tokens.length ? h("ul", { class: "tokens" }, tokens.map((t) => h("li", null,
        h("div", null, h("span", { class: "mono" }, t.ID), h("small", null, `${t.Label ? t.Label + " · " : ""}created ${when(t.Created) || "unknown"} · used ${when(t.LastUsed) || "never"}`)),
        h("button", {
          type: "button", class: "sm danger",
          onclick: async () => {
            if (!await ask(`Revoke ${t.ID}?`, [`${a.Name} can no longer read with this token. This cannot be undone; a new token has to be created.`], "Revoke")) return;
            try { await api("/api/agents/revoke-token", { TokenID: t.ID }); app.say(`revoked ${t.ID}`); } catch (e) { app.say(String(e.message || e), true); }
            render(main, app);
          },
}, "Revoke")))) : h("p", { class: "hint" }, "No tokens yet: make one with New token.")),
    h("div", { class: "card-foot" },
      h("button", { type: "button", class: "sm", onclick: () => view(main, app, a) }, `View as ${a.Name}`),
      h("button", { type: "button", class: "sm primary", onclick: () => newToken(main, app, a) }, "New token")));
}

// newToken makes one more assistant token for an agent and shows it once.
// Nothing else changes: its other tokens and this computer's upload token stay.
async function newToken(main, app, a) {
  if (!await ask(`New token for ${a.Name}?`, ["It is shown once, here; the server keeps only a hash, so it cannot be found again.", `${a.Name}'s other tokens keep working. Nothing else changes.`], "Make the token")) return;
  let t;
  try {
    t = await api("/api/agents/new-token", { AgentID: a.ID });
  } catch (e) {
    app.say(String(e.message || e), true);
    return;
  }
  app.say(`made ${t.ID} for ${a.Name}: copy it now, it is shown once`);
  const copyBtn = (label, value) => {
    const b = h("button", { type: "button", class: "sm" }, label);
    b.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(value); b.textContent = "Copied"; } catch { b.textContent = "Select the text and copy it"; }
      setTimeout(() => { b.textContent = label; }, 1500);
    });
    return b;
  };
  const d = drawer(`Assistant token for ${a.Name}`, [h("span", { class: "badge warn" }, "▲ shown once")], () => render(main, app));
  put(d.body,
    h("p", null, "Copy it into the assistant's connector now. When this panel closes it is gone; if it is lost, make another."),
    h("div", { class: "secret" }, h("code", null, t.Secret), copyBtn("Copy token", t.Secret)),
    h("dl", { class: "kv" },
      h("dt", null, "Connector URL"), h("dd", null, h("code", null, t.URL), " ", copyBtn("Copy", t.URL)),
      h("dt", null, "Header"), h("dd", null, h("code", null, "Authorization: Bearer <token>")),
      h("dt", null, "Token id"), h("dd", { class: "mono" }, t.ID)),
    h("p", { class: "hint" }, `It reads only what ${a.Name} is granted. Revoke it any time from this screen.`));
  put(d.foot, h("button", { type: "button", class: "primary", onclick: () => d.close() }, "I have copied it"));
}

function askSecret(main, app) {
  const secret = h("input", { type: "password", id: "admin-secret", autocomplete: "off" });
  fill(main,
    head("Agents", "Each assistant sees only the projects you grant it."),
    card("Admin secret needed", null, cardBody(
      h("form", {
        onsubmit: async (e) => {
          e.preventDefault();
          try {
            await api("/api/admin", { Secret: secret.value });
            secret.value = "";
            app.say("");
            await render(main, app);
          } catch (err) {
            app.say(String(err.message || err), true);
          }
        },
      }, h("div", { class: "field" }, h("label", { for: "admin-secret" }, "Admin secret"), secret),
      h("div", { class: "row" }, h("button", { type: "submit", class: "primary" }, "Continue"))))),
    note("Seeing and changing who can read what needs the admin secret. It is kept in memory by the local server for this run only: never stored, never sent back to this page."));
  secret.focus();
}

async function view(main, app, a) {
  let v;
  try {
    v = await api(`/api/agents/view?agent=${encodeURIComponent(a.ID)}`);
  } catch (e) {
    app.say(String(e.message || e), true);
    return;
  }
  const ps = v.Projects || [];
  const ss = v.Sessions || [];
  const ws = v.Work || [];
  fill(main,
    head(`What ${a.Name} can read`, v.Note || "The projects this agent can read, and the sessions and work items of the last 5 days in them.",
      h("button", { type: "button", onclick: () => render(main, app) }, "← Agents")),
    card(`Projects (${ps.length})`, null, ps.length
      ? cardBody(h("div", { class: "chips" }, ps.map((p) => h("span", { class: "chip" }, `${p.Name} · ${plural(p.Sessions, "session")}`))))
      : empty(null, null, "None: this agent sees nothing.")),
    card(`Sessions (${ss.length})`, null, ss.length
      ? table(["Project", "Session", "Updated"], ss.map((s) => h("tr", null, h("td", null, s.Project),
        h("td", null, h("span", { class: "title-cell" }, tag(s.Source), h("span", null, s.Title, s.Abstract ? h("small", null, s.Abstract) : null))), h("td", { class: "w faint mono" }, when(s.Updated)))))
      : empty(null, null, "None in the last 5 days.")),
    card(`Work items (${ws.length})`, null, ws.length
      ? table(["Project", "Kind", "Key", "Status"], ws.map((w) => h("tr", null, h("td", null, w.Project), h("td", null, w.Kind), h("td", { class: "mono" }, w.Key), h("td", null, w.Status))))
      : empty(null, null, "None in the last 5 days.")));
}
