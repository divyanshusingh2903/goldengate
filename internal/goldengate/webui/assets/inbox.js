// Inbox: every session of your shared projects, one decision at a time.
// Approving only records the approval; it goes out the next time you send.

import { api } from "/assets/api.js";
import { h, fill, put, when, plural, ask } from "/assets/dom.js";

const CONFLICT = "edit needs review";

function decide(app, main, items, decision, done) {
  return async () => {
    try {
      await api("/api/inbox/decide", { Items: items, Decision: decision });
      app.say(done);
    } catch (e) {
      app.say(String(e.message || e), true);
    }
    await render(main, app);
  };
}

export async function render(main, app) {
  const inbox = await api("/api/inbox");
  const pending = inbox.Pending || [];
  fill(main,
    h("h1", null, "Inbox"),
    h("p", { class: "leaves" }, "Nothing here has been sent by being listed. Approving records your decision; an approved session goes out the next time you send, and a session that changes afterwards comes back here."));

  // offers: only ever offers; nothing changes without a yes
  for (const o of inbox.Offers || []) {
    put(main, h("section", { class: "card" },
      h("h2", null, o.Question),
      o.Because ? h("p", null, o.Because) : null,
      h("p", { class: "dim" }, "Saying yes:"),
      h("ul", null, (o.Effect || []).map((e) => h("li", null, e))),
      h("div", { class: "row" },
        h("button", { type: "button", class: "primary", onclick: () => offer(app, main, o.ID, true) }, "Yes"),
        h("button", { type: "button", onclick: () => offer(app, main, o.ID, false) }, "Not now (do not offer again)"))));
  }

  put(main, h("h2", null, `Waiting for your approval (${pending.length})`));
  if (!pending.length) {
    put(main, h("p", { class: "dim" }, "Nothing is waiting."));
  } else {
    // grouped by project, with one action for a whole group
    const groups = new Map();
    for (const it of pending) {
      const g = groups.get(it.Project) || [];
      g.push(it);
      groups.set(it.Project, g);
    }
    for (const [project, items] of groups) {
      const plain = items.filter((it) => it.State !== CONFLICT);
      put(main, h("div", { class: "row" },
        h("strong", null, project || "(no project)"), h("span", { class: "dim" }, plural(items.length, "session")),
        plain.length > 1 ? h("button", { type: "button", onclick: decide(app, main, plain, "approve", `approved ${plural(plain.length, "session")}: they go out the next time you send`) }, `Approve all ${plain.length}`) : null));
      put(main, table(app, main, items));
    }
  }
  section(app, main, "Excluded: kept on this computer", inbox.Excluded || []);
  section(app, main, "Sent, or queued for the next send", inbox.Sent || []);
  section(app, main, "Retracted: deleted from the server", inbox.Retracted || []);
  put(main, h("div", { class: "row" },
    h("button", { type: "button", onclick: () => app.go("receipts") }, "Receipts: what was sent automatically"),
    h("button", { type: "button", class: "primary", onclick: () => app.go("send") }, "Review and send →")));
}

function section(app, main, title, items) {
  if (!items.length) return;
  put(main, h("h2", null, `${title} (${items.length})`), table(app, main, items, true));
}

async function offer(app, main, id, accept) {
  try {
    app.say((await api("/api/offers/decide", { ID: id, Accept: accept })).Message);
  } catch (e) {
    app.say(String(e.message || e), true);
  }
  await render(main, app);
}

// table lists sessions; outside the per-project groups it names the project.
function table(app, main, items, withProject) {
  return h("table", null,
    h("thead", null, h("tr", null, h("th", null, "Session"), withProject ? h("th", null, "Project") : null, h("th", null, "State"), h("th", null, "Updated"), h("th", null, "Decide"))),
    h("tbody", null, items.map((it) => row(app, main, it, withProject))));
}

function row(app, main, it, withProject) {
  const title = it.Title || it.ID;
  const sent = it.State === "sent" || it.State === "changed";
  const kept = it.State === "excluded" || it.State === "retracted";
  let state = it.State === "changed" ? "changed since sent" : it.State;
  if (it.Edited && it.State !== CONFLICT) state += ", edited by you";
  const actions = [];
  if (it.State === CONFLICT) {
    actions.push(
      h("button", { type: "button", onclick: () => edit(app, main, it) }, "Edit again"),
      h("button", { type: "button", onclick: decide(app, main, [it], "keep-edit", `kept your version of "${title}"`) }, "Keep my version"),
      h("button", { type: "button", onclick: decide(app, main, [it], "drop-edit", `"${title}" uses the new summary`) }, "Use the new summary"));
  } else {
    if (!kept && it.State !== "sent" && it.State !== "queued") actions.push(h("button", { type: "button", class: "primary", onclick: decide(app, main, [it], "approve", `approved "${title}": it goes out the next time you send`) }, "Approve"));
    if (!kept) actions.push(h("button", { type: "button", onclick: decide(app, main, [it], "exclude", `excluded "${title}": it stays on this computer`) }, "Exclude"));
    if (!kept) actions.push(h("button", { type: "button", onclick: () => edit(app, main, it) }, "Edit summary"));
    if (kept) actions.push(h("button", { type: "button", onclick: decide(app, main, [it], "reset", `decision removed: "${title}" is judged afresh`) }, "Undo"));
    if (sent) {
      actions.push(h("button", {
        type: "button",
        onclick: async () => {
          if (await ask("Retract from the server", [`Delete "${title}" from the server and never send it again?`, "Assistants lose it at once. Files on this computer are not touched."], "Retract")) {
            await decide(app, main, [it], "retract", `retracted "${title}": deleted from the server`)();
          }
        },
      }, "Retract"));
    }
  }
  return h("tr", null,
    h("td", null, h("span", { class: "tag" }, it.Source), " ", title, it.Note ? h("div", { class: "dim" }, "Your rules: " + it.Note) : null,
      it.State === CONFLICT ? h("div", { class: "warn" }, "This session changed after you edited its summary. It is held until you decide.") : null),
    withProject ? h("td", null, it.Project) : null,
    h("td", null, state), h("td", null, when(it.Updated)), h("td", null, h("div", { class: "row" }, actions)));
}

// edit opens the summary's form as text. The form file itself stays private
// to the local server; only its text travels to this page and back.
async function edit(app, main, it) {
  let form;
  try {
    form = await api("/api/edit/begin", { Item: it });
  } catch (e) {
    app.say(String(e.message || e), true);
    return;
  }
  const area = h("textarea", { class: "tall mono", "aria-label": "Summary form" }, form.text);
  const finish = async (body, fallback) => {
    try {
      app.say((await api("/api/edit/finish", { ID: form.id, ...body })).Message || fallback);
    } catch (e) {
      app.say(String(e.message || e), true);
    }
    await render(main, app);
  };
  fill(main,
    h("h1", null, `Edit summary: ${it.Title || it.ID}`),
    h("p", { class: "leaves" }, "Your version is what is sent for this session, after the same redaction as everything else, and no summarizer replaces it. Nothing is sent by saving."),
    h("p", { class: "dim" }, "Change the text, delete a line to strike it, delete a work-item line to unlink it. Lines starting with # are notes and are not sent."),
    area,
    h("div", { class: "row" },
      h("button", { type: "button", class: "primary", onclick: () => finish({ Text: area.value }, "saved") }, "Save my version"),
      h("button", { type: "button", onclick: () => finish({ Cancel: true }, "edit cancelled") }, "Cancel")));
  area.focus();
}
