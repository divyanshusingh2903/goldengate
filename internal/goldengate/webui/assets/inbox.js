// Inbox: every session of your shared projects, one decision at a time or
// several at once. Approving only records the approval; it goes out the next
// time you send.

import { api } from "/assets/api.js";
import { h, fill, put, clear, when, plural, ask, tag, head, empty, drawer, docs, more, said, errText, batched } from "/assets/dom.js";

const CONFLICT = "edit needs review";

// data is the last inbox loaded: switching tabs draws from it at once.
const ui = { tab: "waiting", sel: new Set(), data: null };
const keyOf = (it) => `${it.Source}|${it.ID}`;

// badge says a session's state in words, with the colour as a second cue.
function badge(it) {
  const [text, cls] = {
    [CONFLICT]: ["▲ edit needs review", "bad"],
    pending: ["▲ waiting", "warn"],
    queued: ["● queued for the next send", "good"],
    sent: ["✓ sent", "good"],
    changed: ["▲ changed since sent", "warn"],
    excluded: ["– kept on this computer", "dim"],
    retracted: ["✕ deleted from the server", "dim"],
  }[it.State] || [it.State, "dim"];
  return h("span", { class: "badge " + cls }, text, it.Edited && it.State !== CONFLICT ? h("span", { class: "faint" }, " · edited by you") : null);
}

async function decide(app, main, items, decision, done) {
  try {
    await api("/api/inbox/decide", { Items: items, Decision: decision });
    app.say(done);
  } catch (e) {
    app.say(errText(e), true);
  }
  ui.sel.clear();
  await render(main, app);
}

// render loads the inbox and draws it. The counts in the sidebar follow.
export async function render(main, app) {
  const [inbox] = await Promise.all([app.inbox(), app.refresh()]);
  ui.data = inbox;
  draw(main, app);
}

// draw shows the loaded inbox; switching tabs only redraws.
function draw(main, app) {
  const inbox = ui.data || {};
  const lists = {
    waiting: inbox.Pending || [], sent: inbox.Sent || [], excluded: inbox.Excluded || [], retracted: inbox.Retracted || [],
  };
  const tabs = [["waiting", "Waiting"], ["sent", "Sent or queued"], ["excluded", "Kept local"], ["retracted", "Retracted"]];
  const list = lists[ui.tab];
  // a selection only means something for what is on screen
  for (const k of [...ui.sel]) if (!list.some((it) => keyOf(it) === k)) ui.sel.delete(k);
  const plain = lists.waiting.filter((it) => it.State !== CONFLICT);

  fill(main,
    head("Inbox", "New and changed sessions from your shared projects.", docs("terminal-ui#tui-inbox"),
      ui.tab === "waiting" && plain.length > 1
        ? h("button", { type: "button", class: "primary", onclick: () => decide(app, main, plain, "approve", `approved ${plural(plain.length, "session")}: they go out the next time you send`) }, `Approve all ${plain.length}`)
        : null));

  // offers: only ever offers; nothing changes without a yes
  for (const o of inbox.Offers || []) {
    put(main, h("div", { class: "banner" },
      h("div", { class: "grow" },
        h("strong", null, o.Question), o.Because ? h("span", { class: "dim" }, " " + o.Because) : null,
        (o.Effect || []).length ? h("ul", { class: "dim small" }, o.Effect.map((e) => h("li", null, e))) : null),
      h("button", { type: "button", class: "sm", onclick: () => offer(app, main, o.ID, false) }, "Not now"),
      h("button", { type: "button", class: "sm primary", onclick: () => offer(app, main, o.ID, true) }, "Yes")));
  }

  put(main, h("div", { class: "tabs", role: "tablist", "aria-label": "Sessions" }, tabs.map(([id, label]) => h("button", {
    type: "button", role: "tab", "aria-selected": String(ui.tab === id),
    onclick: () => { ui.tab = id; ui.sel.clear(); draw(main, app); },
  }, label, h("span", { class: "c" }, String(lists[id].length))))));

  const pickable = ui.tab === "waiting";
  const bulk = h("div");
  const drawBulk = () => {
    clear(bulk);
    if (!ui.sel.size) return;
    const chosen = list.filter((it) => ui.sel.has(keyOf(it)));
    const approvable = chosen.filter((it) => it.State !== CONFLICT);
    put(bulk, h("div", { class: "bulk" },
      h("span", null, `${ui.sel.size} selected`),
      h("button", { type: "button", class: "sm", onclick: () => decide(app, main, chosen, "exclude", `kept ${plural(chosen.length, "session")} on this computer`) }, "Keep local"),
      approvable.length ? h("button", { type: "button", class: "sm primary", onclick: () => decide(app, main, approvable, "approve", `approved ${plural(approvable.length, "session")}: they go out the next time you send`) }, `Approve ${approvable.length}`) : null,
      h("button", { type: "button", class: "sm icon", "aria-label": "Clear the selection", onclick: () => { ui.sel.clear(); draw(main, app); } }, "✕")));
  };

  if (!list.length) {
    const text = {
      waiting: "Nothing is waiting. New sessions from shared projects show up here as they happen.",
      sent: "Nothing has been sent or queued yet.", excluded: "Nothing is kept local.", retracted: "Nothing was retracted.",
    }[ui.tab];
    put(main, h("div", { class: "card" }, empty(ui.tab === "waiting" ? "✓" : null, null, text)));
  } else {
    const rows = [];
    const groups = new Map();
    for (const it of list) {
      const g = groups.get(it.Project) || [];
      g.push(it);
      groups.set(it.Project, g);
    }
    const all = h("input", { type: "checkbox", "aria-label": "Select every session", checked: list.every((it) => ui.sel.has(keyOf(it))) });
    all.addEventListener("change", () => { for (const it of list) all.checked ? ui.sel.add(keyOf(it)) : ui.sel.delete(keyOf(it)); draw(main, app); });
    for (const [project, items] of groups) {
      const groupPlain = items.filter((it) => it.State !== CONFLICT);
      rows.push(() => h("tr", { class: "group" },
        pickable ? h("td", { class: "w" }) : null,
        h("td", { colspan: "3" }, project || "(no project)", h("span", { class: "faint" }, ` · ${plural(items.length, "session")}`)),
        h("td", { class: "w" }, pickable && groupPlain.length > 1
          ? h("button", { type: "button", class: "sm", onclick: () => decide(app, main, groupPlain, "approve", `approved ${plural(groupPlain.length, "session")}: they go out the next time you send`) }, `Approve ${groupPlain.length}`)
          : null)));
      for (const it of items) rows.push(() => {
        const k = keyOf(it);
        const box = pickable ? h("input", { type: "checkbox", checked: ui.sel.has(k), "aria-label": `Select ${it.Title || it.ID}` }) : null;
        if (box) {
          box.addEventListener("click", (e) => e.stopPropagation());
          box.addEventListener("change", () => { box.checked ? ui.sel.add(k) : ui.sel.delete(k); tr.classList.toggle("sel", box.checked); drawBulk(); });
        }
        const tr = h("tr", { class: "pick" + (ui.sel.has(k) ? " sel" : ""), onclick: () => open(app, main, it) },
          pickable ? h("td", { class: "w" }, box) : null,
          h("td", null, h("span", { class: "title-cell" }, tag(it.Source), h("span", null, it.Title || it.ID,
            it.Note ? h("small", null, "Your rules: " + it.Note) : null))),
          h("td", { class: "w" }, badge(it)),
          h("td", { class: "w faint mono" }, when(it.Updated)),
          h("td", { class: "w" }, h("button", { type: "button", class: "ghost sm", onclick: (e) => { e.stopPropagation(); open(app, main, it); } }, "Open")));
        return tr;
      });
    }
    const body = h("tbody");
    put(main, h("div", { class: "card" }, h("div", { class: "tbl" }, h("table", null,
      h("thead", null, h("tr", null, pickable ? h("th", { class: "w" }, all) : null, h("th", null, "Session"), h("th", null, "State"), h("th", null, "Updated"), h("th", { class: "w" }, h("span", { class: "skip" }, "Open")))),
      body))));
    batched(body, rows, (r) => r());
  }
  put(main, bulk);
  drawBulk();
}

async function offer(app, main, id, accept) {
  try {
    app.say(said(await api("/api/offers/decide", { ID: id, Accept: accept }), accept ? "accepted" : "not now"));
  } catch (e) {
    app.say(errText(e), true);
  }
  await render(main, app);
}

// open shows one session in the drawer, with every decision it allows.
function open(app, main, it) {
  const title = it.Title || it.ID;
  // an edit left open when the drawer closes is cancelled, so its form is not kept
  const editing = { id: "" };
  const d = drawer(title, [tag(it.Source, { name: true }), badge(it)], () => {
    if (editing.id) api("/api/edit/finish", { ID: editing.id, Cancel: true }).catch(() => {});
  });
  d.editing = editing;
  const act = (items, decision, done) => async () => { d.close(); await decide(app, main, items, decision, done); };
  put(d.body,
    it.State === CONFLICT ? h("p", { class: "banner" }, "This session changed after you edited its summary. It is held until you decide.") : null,
    h("dl", { class: "kv" },
      h("dt", null, "Project"), h("dd", null, it.Project || "(no project)"),
      h("dt", null, "Tool"), h("dd", null, it.Source),
      h("dt", null, "Updated"), h("dd", null, when(it.Updated) || "unknown"),
      h("dt", null, "Session"), h("dd", { class: "mono small" }, it.ID),
      it.Note ? [h("dt", null, "Your rules"), h("dd", null, it.Note)] : null));

  const sent = it.State === "sent" || it.State === "changed";
  const kept = it.State === "excluded" || it.State === "retracted";
  const buttons = [];
  if (it.State === CONFLICT) {
    buttons.push(
      h("button", { type: "button", onclick: () => edit(app, main, it, d) }, "Edit again"),
      h("button", { type: "button", onclick: act([it], "drop-edit", `"${title}" uses the new summary`) }, "Use the new summary"),
      h("button", { type: "button", class: "primary", onclick: act([it], "keep-edit", `kept your version of "${title}"`) }, "Keep my version"));
  } else {
    if (!kept) buttons.push(h("button", { type: "button", onclick: () => edit(app, main, it, d) }, "Edit summary"));
    if (sent) {
      buttons.push(h("button", {
        type: "button", class: "danger",
        onclick: async () => {
          if (await ask("Retract from the server", [`Delete "${title}" from the server and never send it again?`, "Assistants lose it at once. Files on this computer are not touched."], "Retract")) {
            await act([it], "retract", `retracted "${title}": deleted from the server`)();
          }
        },
      }, "Retract"));
    }
    if (!kept) buttons.push(h("button", { type: "button", onclick: act([it], "exclude", `excluded "${title}": it stays on this computer`) }, "Keep local"));
    if (kept) buttons.push(h("button", { type: "button", onclick: act([it], "reset", `decision removed: "${title}" is judged afresh`) }, "Undo"));
    if (!kept && it.State !== "sent" && it.State !== "queued") buttons.push(h("button", { type: "button", class: "primary", onclick: act([it], "approve", `approved "${title}": it goes out the next time you send`) }, "Approve"));
  }
  put(d.foot, buttons);
}

// edit opens the summary's form as text in the drawer. The form file itself
// stays private to the local server; only its text travels to this page and back.
async function edit(app, main, it, d) {
  let form;
  try {
    form = await api("/api/edit/begin", { Item: it });
  } catch (e) {
    app.say(errText(e), true);
    return;
  }
  d.editing.id = form.id;
  const area = h("textarea", { class: "tall mono", "aria-label": "Summary form" }, form.text);
  const finish = async (body, fallback) => {
    d.editing.id = "";
    try {
      app.say(said(await api("/api/edit/finish", { ID: form.id, ...body }), fallback));
    } catch (e) {
      app.say(errText(e), true);
    }
    d.close();
    await render(main, app);
  };
  fill(d.body,
    h("p", { class: "hint" }, "Delete a line to strike it. Lines starting with # are not sent."),
    more("Your version is what is sent for this session, after the same redaction as everything else, and no summarizer replaces it. Nothing is sent by saving. Delete a work-item line to unlink it."),
    area);
  fill(d.foot,
    h("button", { type: "button", onclick: () => finish({ Cancel: true }, "edit cancelled") }, "Cancel"),
    h("button", { type: "button", class: "primary", onclick: () => finish({ Text: area.value }, "saved") }, "Save my version"));
  area.focus();
}
