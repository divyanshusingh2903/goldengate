// Agents: which assistant sees which project. Every call here needs the
// admin secret, which the local server keeps in memory for this run only.

import { api } from "/assets/api.js";
import { h, fill, put, when, plural, ask } from "/assets/dom.js";

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

  fill(main,
    h("h1", null, "Agents"),
    h("p", { class: "leaves" }, "Each assistant you connect is an agent. It can read only the projects ticked for it. Ticking a box changes nothing until you review and apply."),
    h("table", null,
      h("thead", null, h("tr", null, h("th", null, "Project"), agents.map((a) => h("th", null, a.Name)))),
      h("tbody", null,
        h("tr", null, h("td", null, h("strong", null, "Every project, including future ones")), agents.map((a) => cell(a, null))),
        projects.map((p) => h("tr", null, h("td", null, p.Name, h("span", { class: "dim" }, ` · ${plural(p.Sessions, "session")}`)), agents.map((a) => cell(a, p)))))));

  if (pending.length) {
    put(main, h("section", { class: "card" },
      h("h2", null, `Review ${plural(pending.length, "change")}`),
      h("ul", null, pending.map((c) => h("li", null, c.Grant
        ? `${c.AgentName} will see ${c.All ? "every project, including future ones" : c.ProjectName}`
        : `${c.AgentName} will no longer see ${c.All ? "every project (only the ones ticked for it)" : c.ProjectName}`))),
      h("div", { class: "row" },
        h("button", {
          type: "button", class: "primary",
          onclick: async () => {
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
        }, "Apply"),
        h("button", { type: "button", onclick: () => { pending = []; render(main, app); } }, "Discard"))));
  }

  put(main, h("h2", null, "Each agent"));
  for (const a of agents) {
    put(main, h("section", { class: "card" },
      h("h2", null, a.Name),
      h("p", { class: "dim" }, [a.AllProjects ? "Sees every project." : `Sees ${plural(Object.keys(a.Grants || {}).length, "project")}.`,
        a.MaxDays ? ` Limited to the last ${a.MaxDays} days.` : "", when(a.LastUsed) ? ` Last used ${when(a.LastUsed)}.` : " Never used.",
        (a.Clients || []).length ? ` Connected applications: ${a.Clients.join(", ")}.` : ""].join("")),
      h("div", { class: "row" }, h("button", { type: "button", onclick: () => view(main, app, a) }, `View as ${a.Name}`)),
      (a.Tokens || []).length ? h("table", null,
        h("thead", null, h("tr", null, h("th", null, "Token"), h("th", null, "Created"), h("th", null, "Last used"), h("th", null, ""))),
        h("tbody", null, a.Tokens.map((t) => h("tr", null,
          h("td", null, h("span", { class: "mono" }, t.ID), t.Label ? ` · ${t.Label}` : ""), h("td", null, when(t.Created)), h("td", null, when(t.LastUsed) || "never"),
          h("td", null, h("button", {
            type: "button",
            onclick: async () => {
              if (!await ask(`Revoke ${t.ID}?`, [`${a.Name} can no longer read with this token. This cannot be undone; a new token has to be created.`], "Revoke")) return;
              try { await api("/api/agents/revoke-token", { TokenID: t.ID }); app.say(`revoked ${t.ID}`); } catch (e) { app.say(String(e.message || e), true); }
              render(main, app);
            },
          }, "Revoke")))))) : h("p", { class: "dim" }, "No tokens.")));
  }
  put(main, h("div", { class: "row" }, h("button", {
    type: "button",
    onclick: async () => { await api("/api/admin/forget", {}); pending = []; app.say("The admin secret was forgotten."); render(main, app); },
  }, "Forget the admin secret")));
}

function askSecret(main, app) {
  const secret = h("input", { type: "password", id: "admin-secret", autocomplete: "off" });
  fill(main,
    h("h1", null, "Agents"),
    h("p", { class: "leaves" }, "Seeing and changing who can read what needs the admin secret. It is kept in memory by the local server for this run only: never stored, never sent back to this page."),
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
    }, h("label", { for: "admin-secret" }, "Admin secret"), secret, h("div", { class: "row" }, h("button", { type: "submit", class: "primary" }, "Continue"))));
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
  fill(main,
    h("div", { class: "row" }, h("button", { type: "button", onclick: () => render(main, app) }, "← Agents")),
    h("h1", null, `What ${a.Name} can read`),
    h("p", { class: "leaves" }, v.Note || "The projects this agent can read, and the sessions and work items of the last 5 days in them."),
    h("h2", null, `Projects (${(v.Projects || []).length})`),
    (v.Projects || []).length ? h("ul", null, v.Projects.map((p) => h("li", null, `${p.Name} · ${plural(p.Sessions, "session")}`))) : h("p", { class: "dim" }, "None: this agent sees nothing."),
    h("h2", null, `Sessions (${(v.Sessions || []).length})`),
    (v.Sessions || []).length ? h("table", null,
      h("thead", null, h("tr", null, h("th", null, "Project"), h("th", null, "Session"), h("th", null, "Updated"))),
      h("tbody", null, v.Sessions.map((s) => h("tr", null, h("td", null, s.Project),
        h("td", null, h("span", { class: "tag" }, s.Source), " ", s.Title, s.Abstract ? h("div", { class: "dim" }, s.Abstract) : null), h("td", null, when(s.Updated)))))) : h("p", { class: "dim" }, "None in the last 5 days."),
    h("h2", null, `Work items (${(v.Work || []).length})`),
    (v.Work || []).length ? h("table", null,
      h("thead", null, h("tr", null, h("th", null, "Project"), h("th", null, "Kind"), h("th", null, "Key"), h("th", null, "Status"))),
      h("tbody", null, v.Work.map((w) => h("tr", null, h("td", null, w.Project), h("td", null, w.Kind), h("td", { class: "mono" }, w.Key), h("td", null, w.Status))))) : h("p", { class: "dim" }, "None in the last 5 days."));
}
