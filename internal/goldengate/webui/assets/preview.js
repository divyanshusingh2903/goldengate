// Preview: exactly what would be sent for one project, redactions marked.

import { api } from "/assets/api.js";
import { h, when, plural, marked, put, fill, tag, head, card, cardBody, note, empty } from "/assets/dom.js";

export async function render(main, app, params) {
  if (!params.key) { app.go("projects"); return; }
  const pv = await api(`/api/preview?limit=20&key=${encodeURIComponent(params.key)}`);
  const items = pv.Items || [];
  const reds = Object.entries(pv.Redactions || {}).map(([k, n]) => `${k} ×${n}`).join(", ");
  fill(main,
    head(`Preview: ${params.label || params.key}`,
      `${plural(pv.Total || items.length, "session")} in this project${items.length < (pv.Total || 0) ? `, the newest ${items.length} shown` : ""}. ${reds ? `Redacted: ${reds}.` : "Nothing needed redacting."}${pv.Warnings ? ` ${plural(pv.Warnings, "warning")}.` : ""}`,
      h("button", { type: "button", onclick: () => app.go("projects") }, "← Projects")),
    note("This is the exact text that would leave this computer for this project, at the \"" + (pv.Level || "") + "\" level. Looking at it sends nothing and records nothing."),
  );
  if (!items.length) {
    put(main, h("div", { class: "card gap-top" }, empty(null, "No sessions to show", "This project has no sessions in the look-back window.")));
    return;
  }
  const list = h("div", { class: "gap-top" });
  for (const it of items) {
    const status = { new: "new", changed: "changed since it was last sent", unchanged: "already sent, unchanged" }[it.Status] || it.Status;
    const cls = { new: "warn", changed: "warn", unchanged: "good" }[it.Status] || "dim";
    put(list, card(h("span", { class: "title-cell" }, tag(it.Source), h("span", { class: "badge " + cls }, status)),
      when(it.Updated) ? h("span", { class: "faint mono small" }, when(it.Updated)) : null,
      cardBody(
        (it.Diff || []).length ? h("div", null, h("p", { class: "hint" }, "Since it was last sent:"), h("pre", null, it.Diff.join("\n"))) : null,
        (it.Diff || []).length ? h("p", { class: "hint gap-top" }, "What would be sent:") : null,
        h("pre", null, marked(it.JSON || "")))));
  }
  put(main, list);
}
