// Overview: what waits on you, what is shared, and what your server holds.
// It only reads; every action is on the screen it links to.

import { api } from "/assets/api.js";
import { h, fill, head, card, cardBody, stat, empty, table, when, plural, tag, docs, errText } from "/assets/dom.js";

export async function render(main, app) {
  const [snap, inbox, server, receipts, settings] = await Promise.all([
    app.refresh(),
    app.inbox().catch(() => ({})),
    api("/api/server").catch(() => null),
    api("/api/receipts").catch(() => []),
    api("/api/settings").catch(() => ({})),
  ]);
  const rows = snap.Rows || [];
  const shared = rows.filter((r) => r.State === 1 || r.State === 2);
  const toSend = shared.reduce((n, r) => n + (r.Pending || 0), 0);
  const pending = inbox.Pending || [];
  const held = (server && server.Projects) || [];
  const onServer = held.reduce((n, p) => n + (p.Sessions || 0), 0);

  let serverStat;
  if (!server || !server.Configured) serverStat = stat("On the server", "—", "no server connected");
  else if (!server.Healthy) serverStat = stat("On the server", "✕ unreachable", (server.URL || "").replace(/^https?:\/\//, ""), true);
  else serverStat = stat("On the server", String(onServer), `sessions in ${plural(held.length, "project")}`);

  const waiting = pending.length
    ? table(null, pending.slice(0, 5).map((it) => h("tr", { class: "pick", onclick: () => app.go("inbox") },
      h("td", { class: "w" }, tag(it.Source)),
      h("td", null, it.Title || it.ID, h("small", null, it.Project || "")),
      h("td", { class: "w faint mono" }, when(it.Updated)))))
    : empty("✓", null, "Nothing is waiting for a decision.");

  const recent = (receipts || []).slice(0, 4);
  const activity = recent.length
    ? cardBody(h("ul", { class: "tl" }, recent.map((r) => {
      const items = r.Items || [];
      return h("li", null,
        h("span", { class: "when" }, when(r.At)),
        h("span", { class: "pt", "aria-hidden": "true" }),
        h("div", null, h("div", { class: "what" }, `${plural(items.length, "session")} sent automatically`),
          h("div", { class: "det" }, items[0] ? `${items[0].Project}: ${items[0].Why || "a rule you approved"}` : "")));
    })))
    : empty(null, null, "Nothing was sent automatically. A project sends on its own only at trust level auto.");

  // asked once, when a project already sends on its own and nothing was chosen
  const daemonAsk = (settings.Daemon || {}).Ask
    ? card("Send automatically in the background?", null,
      cardBody(h("p", null, "Sends only what you or your rules approved, so nothing depends on remembering to send. Each send gets a receipt."),
        h("div", { class: "row gap-top" },
          h("button", { type: "button", class: "primary", onclick: (e) => chooseDaemon(app, e.currentTarget, true) }, "Turn on"),
          h("button", { type: "button", onclick: (e) => chooseDaemon(app, e.currentTarget, false) }, "Not now"))))
    : null;

  fill(main,
    head("Overview", "What your assistants can see, and what is waiting on you.", docs("web-app#web-screens"),
      h("button", { type: "button", onclick: () => app.go("inbox") }, "Open inbox"),
      snap.Server ? null : h("button", { type: "button", class: "primary", onclick: () => app.go("setup") }, "Connect a server")),
    daemonAsk,
    h("div", { class: "grid g4" },
      stat("Waiting for you", String(pending.length), "in the inbox"),
      stat("Shared projects", String(shared.length), `of ${rows.length} on this computer`),
      stat("To send", String(toSend), toSend ? "new or changed sessions" : "everything is sent"),
      serverStat),
    h("div", { class: "grid g2 gap-top" },
      card("Needs a decision", h("button", { type: "button", class: "ghost sm", onclick: () => app.go("inbox") }, "View all"), waiting),
      card("Sent automatically", h("button", { type: "button", class: "ghost sm", onclick: () => app.go("receipts") }, "Receipts"), activity)));
}

// chooseDaemon answers the question: on installs the background service, not
// now is remembered and asks again never. The setting changes only if the
// service could be installed; an error says why and leaves the question.
async function chooseDaemon(app, button, on) {
  button.disabled = true;
  try {
    const r = await api("/api/daemon", { On: on });
    app.say(on ? (r.warning ? "Background sending is on, but " + r.warning.split("\n")[0] : "Background sending is on.") : "Not now. You can turn it on in Settings.", on && r.warning ? "warn" : "ok");
    await app.go("overview");
  } catch (e) {
    button.disabled = false;
    app.say(errText(e), true);
  }
}
