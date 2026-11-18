// Server: whether it is reachable, and what it holds.

import { api } from "/assets/api.js";
import { h, when, ask, plural, put, fill, head, card, cardBody, note, stat, empty, table } from "/assets/dom.js";

export async function render(main, app) {
  const info = await api("/api/server");
  if (!info.Configured) {
    fill(main, head("Server", "Your Golden Gate Bridge."),
      h("div", { class: "card" }, empty("○", "No server connected", "Nothing can leave this computer until you connect one.",
        h("button", { type: "button", class: "primary", onclick: () => app.go("setup") }, "Connect to your server"))));
    return;
  }
  const projects = info.Projects || [];
  const sessions = projects.reduce((n, p) => n + (p.Sessions || 0), 0);
  const copied = h("span", { class: "hint" });
  fill(main,
    head("Server", "Your Golden Gate Bridge.", h("button", { type: "button", onclick: () => render(main, app).then(() => app.say("Checked the server again.")) }, "Check again")),
    h("div", { class: "grid g4" },
      stat("Status", h("span", { class: info.Healthy ? "good" : "bad" }, info.Healthy ? "● Answers" : "✕ Unreachable"), info.Healthy ? "the server is up" : "check the address and the network", true),
      stat("Upload token", h("span", { class: info.TokenOK ? "good" : "bad" }, info.TokenOK ? "● Accepted" : "✕ Refused"), info.TokenStore ? `kept in the ${info.TokenStore}` : "", true),
      stat("Projects held", String(projects.length), "on the server"),
      stat("Sessions held", String(sessions), "across those projects")),
    info.Err ? h("p", { class: "banner gap-top bad" }, info.Err) : null,
    card("Connection", null, cardBody(
      h("div", { class: "field" }, h("label", { for: "srv-url" }, "Server address"),
        h("div", { class: "row" }, h("span", { class: "grow-field" }, h("input", { type: "text", id: "srv-url", class: "mono", value: info.URL, readOnly: true })),
          h("button", { type: "button", onclick: async () => { try { await navigator.clipboard.writeText(info.URL); copied.textContent = "copied"; } catch { copied.textContent = "select the text and copy it"; } } }, "Copy"), copied)),
      h("p", { class: "hint gap-top" }, "To connect to a different server or make new tokens, use Setup."))));
  const held = card("What the server holds", null,
    projects.length
      ? table(["Project", h("th", { class: "num" }, "Sessions"), "Last activity", h("th", { class: "w" }, h("span", { class: "skip" }, "Delete"))], projects.map((p) => h("tr", null,
        h("td", null, p.Label), h("td", { class: "num" }, String(p.Sessions)), h("td", { class: "faint mono" }, when(p.Last)),
        h("td", { class: "w" }, h("button", {
          type: "button", class: "sm danger",
          onclick: async () => {
            if (!await ask(`Delete ${p.Label} from the server?`, [`${plural(p.Sessions, "session")} will be deleted from the server.`, "If the project is still shared, the next send uploads it again."], "Delete from the server")) return;
            try { await api("/api/server/delete-project", { Ref: p.ID || p.Label }); app.say(`Deleted ${p.Label} from the server.`); } catch (e) { app.say(String(e.message || e), true); }
            render(main, app);
          },
        }, "Delete")))))
      : empty(null, null, "Nothing yet."));
  held.classList.add("gap-top");
  put(main, held,
    note("This is everything of yours on the server. Deleting a project removes it for every assistant at once; files on this computer are not touched."));
}
