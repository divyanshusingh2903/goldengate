// Preview: exactly what would be sent for one project, redactions marked.

import { api } from "/assets/api.js";
import { h, when, plural, marked, put, fill, tag } from "/assets/dom.js";

export async function render(main, app, params) {
  if (!params.key) { app.go("projects"); return; }
  const pv = await api(`/api/preview?limit=20&key=${encodeURIComponent(params.key)}`);
  const items = pv.Items || [];
  const reds = Object.entries(pv.Redactions || {}).map(([k, n]) => `${k} ×${n}`).join(", ");
  fill(main, 
    h("div", { class: "row" }, h("button", { type: "button", onclick: () => app.go("projects") }, "← Projects")),
    h("h1", null, `Preview: ${params.label || params.key}`),
    h("p", { class: "leaves" }, "This is the exact text that would leave this computer for this project, at the \"" + (pv.Level || "") + "\" level. Looking at it sends nothing and records nothing."),
    h("p", { class: "dim" }, `${plural(pv.Total || items.length, "session")} in this project${items.length < (pv.Total || 0) ? `, the newest ${items.length} shown` : ""}. `,
      reds ? `Redacted: ${reds}.` : "Nothing needed redacting.", pv.Warnings ? ` ${plural(pv.Warnings, "warning")}.` : ""),
  );
  if (!items.length) {
    put(main, h("p", null, "No sessions to show."));
    return;
  }
  for (const it of items) {
    const status = { new: "new", changed: "changed since it was last sent", unchanged: "already sent, unchanged" }[it.Status] || it.Status;
    put(main, h("section", { class: "card" },
      h("h2", null, tag(it.Source), " ", status, when(it.Updated) ? ` · ${when(it.Updated)}` : ""),
      (it.Diff || []).length ? h("div", null, h("p", { class: "dim" }, "Since it was last sent:"), h("pre", null, it.Diff.join("\n"))) : null,
      h("pre", null, marked(it.JSON || ""))));
  }
}
