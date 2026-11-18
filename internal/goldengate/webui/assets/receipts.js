// Receipts: what your projects' trust level and rules sent without asking,
// and why. Any of it can be taken back.

import { api } from "/assets/api.js";
import { h, fill, put, plural, ask, tag, when, head, card, note, empty, table } from "/assets/dom.js";

export async function render(main, app) {
  // a background send or a retract changes the counts, so refresh them too
  const [receipts] = await Promise.all([api("/api/receipts"), app.refresh()]);
  fill(main,
    head("Receipts", "Every session sent automatically, with the rule that sent it."));
  if (!(receipts || []).length) {
    put(main, h("div", { class: "card" }, empty(null, "No receipts", "Nothing was sent automatically. A project sends on its own only at trust level auto.")));
  }
  for (const r of receipts || []) {
    const items = r.Items || [];
    put(main, card(
      h("div", null, h("h2", null, `${plural(items.length, "session")} sent automatically`), h("small", { class: "faint mono" }, `${new Date(r.At).toLocaleString()} · ${r.ID}`)),
      h("span", { class: "faint mono small" }, when(r.At)),
      table(["Session", "Project", "Sent because", h("th", { class: "w" }, h("span", { class: "skip" }, "Retract"))], items.map((it) => h("tr", null,
        h("td", null, h("span", { class: "title-cell" }, tag(it.Source), h("span", null, it.Title || it.ID))),
        h("td", null, it.Project),
        h("td", { class: "dim" }, it.Why || ""),
        h("td", { class: "w" }, it.Retracted ? h("span", { class: "dim" }, "✕ retracted") : h("button", {
          type: "button", class: "sm danger",
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
  put(main, note("Sessions you approved yourself are not listed here; they are in the inbox."));
}
