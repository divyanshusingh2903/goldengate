// Receipts: what your projects' trust level and rules sent without asking,
// and why. Any of it can be taken back.

import { api } from "/assets/api.js";
import { h, fill, put, plural, ask, tag, head, card, empty, table, stamp, stat, drawer, docs, errText } from "/assets/dom.js";

const ui = { query: "" };

const uniq = (xs) => [...new Set(xs.filter(Boolean))];

// few is a short list in words: "Forge, App +2".
function few(xs, n) {
  const u = uniq(xs);
  return u.length <= n ? u.join(", ") : `${u.slice(0, n).join(", ")} +${u.length - n}`;
}

export async function render(main, app) {
  // a background send or a retract changes the counts, so refresh them too
  app.refresh().catch(() => {});
  const receipts = (await api("/api/receipts")) || [];
  const items = receipts.flatMap((r) => r.Items || []);
  const retracted = items.filter((it) => it.Retracted).length;

  if (!receipts.length) {
    fill(main,
      head("Receipts", "What your rules sent without asking.", docs("automation#receipts")),
      h("div", { class: "card" }, empty("○", "Nothing was sent automatically", "A project sends on its own only at trust level auto.",
        h("button", { type: "button", class: "primary", onclick: () => app.go("rules") }, "Open Rules"))));
    return;
  }

  const search = h("input", { type: "search", value: ui.query, placeholder: "Search sessions, projects or rules", "aria-label": "Search receipts" });
  const body = h("tbody");
  const tableBox = h("div", { class: "card" }, h("div", { class: "tbl" }, h("table", null,
    h("thead", null, h("tr", null, h("th", null, "Sent"), h("th", { class: "num" }, "Sessions"), h("th", null, "Projects"), h("th", null, "Sent because"), h("th", null, "State"), h("th", { class: "w" }, h("span", { class: "skip" }, "Open")))),
    body)));
  const draw = () => {
    const q = ui.query.trim().toLowerCase();
    const match = (r) => !q || (r.Items || []).some((it) => [it.Title, it.ID, it.Project, it.Why].some((v) => String(v || "").toLowerCase().includes(q)));
    const vis = receipts.filter(match);
    fill(body, vis.length ? vis.map((r) => {
      const its = r.Items || [];
      const back = its.filter((it) => it.Retracted).length;
      const openIt = () => open(app, main, r);
      return h("tr", { class: "pick", onclick: openIt },
        h("td", { class: "w" }, stamp(r.At)),
        h("td", { class: "num" }, String(its.length)),
        h("td", null, few(its.map((it) => it.Project), 2)),
        h("td", { class: "dim" }, few(its.map((it) => it.Why), 1)),
        h("td", { class: "w" }, back === its.length ? h("span", { class: "badge dim" }, "✕ all retracted") : back ? h("span", { class: "badge warn" }, `▲ ${back} retracted`) : h("span", { class: "badge good" }, "✓ on the server")),
        h("td", { class: "w" }, h("button", { type: "button", class: "ghost sm", onclick: (e) => { e.stopPropagation(); openIt(); } }, "Open")));
    }) : h("tr", null, h("td", { colspan: "6" }, empty(null, null, "No receipt matches."))));
  };
  search.addEventListener("input", () => { ui.query = search.value; draw(); });

  fill(main,
    head("Receipts", "What your rules sent without asking.", docs("automation#receipts"),
      h("button", { type: "button", onclick: () => app.go("rules") }, "Rules")),
    h("div", { class: "grid g3 stats" },
      stat("Automatic sends", String(receipts.length), receipts.length >= 50 ? "the latest 50" : "in total"),
      stat("Sessions sent", String(items.length), plural(uniq(items.map((it) => it.Project)).length, "project")),
      stat("Retracted", String(retracted), "deleted from the server again")),
    h("div", { class: "toolbar gap-top" }, search),
    tableBox);
  draw();
}

// open shows one receipt: every session it sent, why, and retract.
function open(app, main, r) {
  const its = r.Items || [];
  const d = drawer(`${plural(its.length, "session")} sent automatically`, [stamp(r.At), h("span", { class: "faint mono small" }, r.ID)], null, { wide: true });
  const retract = async (it) => {
    const title = it.Title || it.ID;
    if (!await ask("Retract from the server", [`Delete "${title}" from the server and never send it again?`, "Assistants lose it at once. Files on this computer are not touched."], "Retract")) return;
    try {
      await api("/api/inbox/decide", { Items: [{ Source: it.Source, ID: it.ID, Project: it.Project, Title: it.Title }], Decision: "retract" });
      it.Retracted = true;
      app.say(`retracted "${title}": deleted from the server`);
    } catch (e) {
      app.say(errText(e), true);
    }
    drawItems();
    render(main, app);
  };
  const box = h("div");
  const drawItems = () => fill(box, card(null, null, table(["Session", "Project", "Sent because", h("th", { class: "w" }, h("span", { class: "skip" }, "Retract"))], its.map((it) => h("tr", null,
    h("td", null, h("span", { class: "title-cell" }, tag(it.Source), h("span", null, it.Title || it.ID, h("small", { class: "mono" }, it.ID)))),
    h("td", null, it.Project),
    h("td", { class: "dim" }, it.Why || ""),
    h("td", { class: "w" }, it.Retracted ? h("span", { class: "dim" }, "✕ retracted") : h("button", { type: "button", class: "sm danger", onclick: () => retract(it) }, "Retract")))))));
  drawItems();
  put(d.body, box);
  put(d.foot, h("button", { type: "button", class: "primary", onclick: () => d.close() }, "Done"));
}
