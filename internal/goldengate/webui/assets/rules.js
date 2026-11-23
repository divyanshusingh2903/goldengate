// Rules: per project, how far its automation goes (the trust level) and the
// rules that approve or exclude its sessions by facts, never by model output.

import { api } from "/assets/api.js";
import { h, fill, put, ask, head, card, empty, table, drawer, docs, said, errText, TOOLS, toolName, tag } from "/assets/dom.js";

const ui = { view: "", query: "" };

const TRUST = [
  ["ask", "Ask", "Everything waits in the inbox."],
  ["suggest", "Suggest", "What your rules approve is marked in the inbox."],
  ["auto", "Auto", "What your rules approve is sent with the next send."],
];

// conditions are a rule's facts as chips: "tool Codex", "branch fix/*".
function conditions(r) {
  const c = [];
  if (r.Source) c.push(h("span", { class: "chip" }, h("span", { class: "faint" }, "tool"), tag(r.Source, { name: true })));
  for (const [k, v] of [["folder", r.Folder], ["remote", r.Remote], ["branch", r.Branch], ["work item", r.WorkItem]]) {
    if (v) c.push(h("span", { class: "chip" }, h("span", { class: "faint" }, k), h("code", null, v)));
  }
  return c.length ? h("div", { class: "chips" }, c) : h("span", { class: "dim" }, "every session of the project");
}

function actionBadge(a) {
  return a === "exclude" ? h("span", { class: "badge bad" }, "– exclude") : h("span", { class: "badge good" }, "✓ approve");
}

export async function render(main, app) {
  const list = (await api("/api/rules")) || [];
  const withRules = list.filter((p) => (p.Rules || []).length || p.Trust !== "ask");
  if (!ui.view) ui.view = withRules.length ? "set" : "all";

  const reload = () => render(main, app);
  const trustTo = async (p, level) => {
    if (level === p.Trust) return;
    if (level === "auto" && !await ask(`Send ${p.Name} automatically?`,
      [(p.Rules || []).some((r) => r.Action === "approve") ? "Sessions your approve rules match go out with the next send, without waiting in the inbox." : "Every new and changed session of this project goes out with the next send, without waiting in the inbox.",
        "Each automatic send gets a receipt, and anything sent can be retracted."], "Turn on auto")) return;
    try { app.say(said(await api("/api/rules/trust", { Project: p.Name, Trust: level }), `${p.Name}: trust ${level}`)); } catch (e) { app.say(errText(e), true); }
    reload();
  };
  const remove = async (p, r) => {
    if (!await ask("Remove this rule?", [`${p.Name}: ${r.Words || r.ID}`], "Remove")) return;
    try { app.say(said(await api("/api/rules/remove", { Project: p.Name, ID: r.ID }), "removed")); } catch (e) { app.say(errText(e), true); }
    reload();
  };

  const chip = (id, label, n) => h("button", { type: "button", "aria-pressed": String(ui.view === id), onclick: () => { ui.view = id; reload(); } }, `${label} ${n}`);
  const search = h("input", { type: "search", value: ui.query, placeholder: "Search projects", "aria-label": "Search projects" });
  search.addEventListener("input", () => { ui.query = search.value; drawCards(); });

  const cards = h("div", { class: "rule-cards" });
  const projectCard = (p) => {
    const seg = h("div", { class: "seg", role: "group", "aria-label": `Trust level of ${p.Name}` },
      TRUST.map(([id, label, hint]) => h("button", { type: "button", title: hint, "aria-pressed": String(p.Trust === id), onclick: () => trustTo(p, id) }, label)));
    const members = p.Members || [];
    const rs = p.Rules || [];
    return card(
      h("div", null, h("h2", null, p.Name), members.length ? h("small", { class: "faint mono" }, members.slice(0, 2).join(" · "), members.length > 2 ? ` · +${members.length - 2}` : "") : null),
      seg,
      h("p", { class: "card-body trust-words dim small" }, p.Words),
      rs.length
        ? table(["Does", "When a session matches", h("th", { class: "w" }, h("span", { class: "skip" }, "Remove"))], rs.map((r) => h("tr", null,
          h("td", { class: "w" }, actionBadge(r.Action)),
          h("td", null, conditions(r), h("small", { class: "mono" }, r.ID)),
          h("td", { class: "w" }, h("button", { type: "button", class: "sm ghost danger", onclick: () => remove(p, r) }, "Remove")))))
        : null,
      h("div", { class: "card-foot" }, h("button", { type: "button", class: "sm", onclick: () => addRule(app, p, reload) }, "+ Add rule")));
  };
  function drawCards() {
    const q2 = ui.query.trim().toLowerCase();
    const vis = (ui.view === "set" ? withRules : list).filter((p) => !q2 || p.Name.toLowerCase().includes(q2) || (p.Members || []).some((m) => m.toLowerCase().includes(q2)));
    fill(cards, vis.length ? vis.map(projectCard) : h("div", { class: "card" }, empty(null, null, list.length ? "No project matches." : "")));
  }

  if (!list.length) {
    fill(main, head("Rules", "What each project sends on its own.", docs("automation#rules")),
      h("div", { class: "card" }, empty("○", "No named projects yet", "Rules belong to a project. Share a folder first.",
        h("button", { type: "button", class: "primary", onclick: () => app.go("projects") }, "Go to Projects"))));
    return;
  }
  fill(main,
    head("Rules", "What each project sends on its own.", docs("automation#rules"),
      h("button", { type: "button", class: "primary", onclick: () => addRule(app, null, reload, list) }, "New rule")),
    h("div", { class: "toolbar" },
      h("div", { class: "seg", role: "group", "aria-label": "Show" }, chip("set", "Automated", withRules.length), chip("all", "All projects", list.length)),
      search),
    cards);
  drawCards();
}

// addRule opens the form for a new rule. With no project given, the form
// starts by asking which one.
function addRule(app, p, done, all) {
  const d = drawer(p ? `New rule for ${p.Name}` : "New rule", [docs("automation#rules")]);
  const pick = p ? null : h("select", { id: "rule-project" }, (all || []).map((x) => h("option", { value: x.Name }, x.Name)));
  const action = (v, label) => h("label", { class: "check" }, h("input", { type: "radio", name: "rule-action", value: v, checked: v === "approve" }), label);
  const source = h("select", { id: "rule-source" }, h("option", { value: "" }, "Any tool"), TOOLS.map((t) => h("option", { value: t }, toolName(t))));
  const folders = h("datalist", { id: "rule-folders" });
  const drawFolders = () => {
    const proj = p || (all || []).find((x) => x.Name === pick.value) || {};
    fill(folders, (proj.Members || []).map((m) => h("option", { value: m })));
  };
  if (pick) pick.addEventListener("change", drawFolders);
  drawFolders();
  const text = (id, label, placeholder, list) => {
    const input = h("input", { type: "text", id, placeholder, autocomplete: "off", spellcheck: false, list });
    return [input, h("div", { class: "field" }, h("label", { for: id }, label), input)];
  };
  const [folder, folderF] = text("rule-folder", "Folder or chat", "~/code/app, ~/work/*, chatgpt:*", "rule-folders");
  const [remote, remoteF] = text("rule-remote", "Repository remote", "github.com/acme/*");
  const [branch, branchF] = text("rule-branch", "Git branch", "fix/*");
  const [work, workF] = text("rule-work", "Issue or pull request", "linear:ENG, acme/app");
  const submit = h("button", { type: "submit", class: "primary", form: "rule-form" }, "Add rule");
  const form = h("form", {
    id: "rule-form",
    onsubmit: async (e) => {
      e.preventDefault();
      const act = form.querySelector('input[name="rule-action"]:checked').value;
      const rule = { Action: act, Source: source.value, Folder: folder.value.trim(), Remote: remote.value.trim(), Branch: branch.value.trim(), WorkItem: work.value.trim() };
      submit.disabled = true;
      try {
        app.say(said(await api("/api/rules/add", { Project: p ? p.Name : pick.value, Rule: rule }), "rule added"));
        d.close();
        done();
      } catch (err) {
        app.say(errText(err), true);
        submit.disabled = false;
      }
    },
  },
  pick ? h("div", { class: "field" }, h("label", { for: "rule-project" }, "Project"), pick) : null,
  h("fieldset", { class: "field plain" }, h("legend", null, "Matching sessions are"), h("div", { class: "row" }, action("approve", "Approved"), action("exclude", "Kept on this computer"))),
  h("p", { class: "hint gap-top" }, "A session must match every condition you fill in."),
  h("div", { class: "field gap-top" }, h("label", { for: "rule-source" }, "Tool"), source),
  folderF, folders, remoteF, branchF, workF);
  put(d.body, form);
  put(d.foot, h("button", { type: "button", onclick: () => d.close() }, "Cancel"), submit);
}
