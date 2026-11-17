// Receipts: what your projects' trust level and rules sent without asking,
// and why. Any of it can be taken back.

import { api } from "/assets/api.js";
import { h, fill, put, plural, ask } from "/assets/dom.js";

export async function render(main, app) {
  const receipts = await api("/api/receipts");
  fill(main,
    h("h1", null, "Receipts"),
    h("p", { class: "leaves" }, "Every session sent automatically is listed here with the rule that sent it. Sessions you approved yourself are not; they are in the inbox."));
  if (!(receipts || []).length) {
    put(main, h("p", { class: "dim" }, "No receipts: nothing was sent automatically. A project sends on its own only at trust level auto."));
    return;
  }
  for (const r of receipts) {
    const items = r.Items || [];
    put(main,
      h("h2", null, `${new Date(r.At).toLocaleString()} · ${plural(items.length, "session")} sent automatically`),
      h("p", { class: "dim mono" }, r.ID),
      h("table", null,
        h("thead", null, h("tr", null, h("th", null, "Session"), h("th", null, "Project"), h("th", null, "Sent because"), h("th", null, ""))),
        h("tbody", null, items.map((it) => h("tr", null,
          h("td", null, h("span", { class: "tag" }, it.Source), " ", it.Title || it.ID),
          h("td", null, it.Project),
          h("td", null, it.Why || ""),
          h("td", null, it.Retracted ? h("span", { class: "dim" }, "✕ retracted") : h("button", {
            type: "button",
            onclick: async () => {
              const title = it.Title || it.ID;
              if (!await ask("Retract from the server", [`Delete "${title}" from the server and never send it again?`, "Assistants lose it at once. Files on this computer are not touched."], "Retract")) return;
              try {
                await api("/api/inbox/decide", { Items: [{ Source: it.Source, ID: it.ID, Project: it.Project, Title: it.Title }], Decision: "retract" });
                app.say(`retracted "${title}": deleted from the server`);
              } catch (e) {
                app.say(String(e.message || e), true);
              }
              await render(main, app);
            },
          }, "Retract")))))));
  }
}
