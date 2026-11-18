// Preview: exactly what would be sent for one project, redactions marked.
// Each session opens as it will be read, with the exact JSON a tab away.

import { api } from "/assets/api.js";
import { h, when, plural, put, fill, tag, head, card, note, empty, drawer } from "/assets/dom.js";
import { sessionView } from "/assets/session.js";

const STATUS = {
  new: ["● new", "good"], changed: ["▲ changed since it was last sent", "warn"], unchanged: ["✓ already sent, unchanged", "dim"],
};

function badge(status) {
  const [text, cls] = STATUS[status] || [status, "dim"];
  return h("span", { class: "badge " + cls }, text);
}

function open(it) {
  const p = it.Payload || {};
  const title = (p.summary && p.summary.title) || p.title_only || it.ID;
  const d = drawer(title, [tag(it.Source, { name: true }), badge(it.Status)], null, { wide: true });
  put(d.body,
    (it.Diff || []).length ? h("div", { class: "sv-sec" }, h("h4", null, "Since it was last sent"), h("pre", null, it.Diff.join("\n"))) : null,
    sessionView(p, it.JSON || ""));
  put(d.foot, h("button", { type: "button", class: "primary", onclick: () => d.close() }, "Done"));
}

export async function render(main, app, params) {
  if (!params.key) { app.go("projects"); return; }
  const pv = await api(`/api/preview?limit=50&key=${encodeURIComponent(params.key)}`);
  const items = pv.Items || [];
  const reds = Object.entries(pv.Redactions || {}).map(([k, n]) => `${k} ×${n}`).join(", ");
  fill(main,
    head(`Preview: ${params.label || params.key}`,
      `${plural(pv.Total || items.length, "session")} in this project${items.length < (pv.Total || 0) ? `, the newest ${items.length} shown` : ""}. ${reds ? `Redacted: ${reds}.` : "Nothing needed redacting."}${pv.Warnings ? ` ${plural(pv.Warnings, "warning")}.` : ""}`,
      h("button", { type: "button", onclick: () => app.go("projects") }, "← Projects")));
  if (!items.length) {
    put(main, h("div", { class: "card" }, empty(null, "No sessions to show", "This project has no sessions in the look-back window.")));
    return;
  }
  const rows = items.map((it) => {
    const p = it.Payload || {};
    const s = p.summary || {};
    return h("tr", { class: "pick", onclick: () => open(it) },
      h("td", null, h("span", { class: "title-cell" }, tag(it.Source), h("span", null, s.title || p.title_only || it.ID,
        s.abstract ? h("small", null, s.abstract) : null))),
      h("td", { class: "w" }, badge(it.Status)),
      h("td", { class: "w faint mono" }, when(it.Updated)),
      h("td", { class: "w" }, h("button", { type: "button", class: "ghost sm", onclick: (e) => { e.stopPropagation(); open(it); } }, "Read")));
  });
  put(main,
    card(plural(items.length, "session"), h("span", { class: "faint small" }, `share level: ${pv.Level || "summary"}`),
      h("div", { class: "tbl" }, h("table", null,
        h("thead", null, h("tr", null, h("th", null, "Session"), h("th", null, "State"), h("th", null, "Updated"), h("th", { class: "w" }, h("span", { class: "skip" }, "Read")))),
        h("tbody", null, rows)))),
    note("Open a session to read exactly what would leave this computer for it. Looking sends nothing and records nothing."));
}
