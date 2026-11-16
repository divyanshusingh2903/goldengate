// Server: whether it is reachable, and what it holds.

import { api } from "/assets/api.js";
import { h, when, ask, plural, put, fill } from "/assets/dom.js";

export async function render(main, app) {
  const info = await api("/api/server");
  fill(main, h("h1", null, "Server"));
  if (!info.Configured) {
    put(main, h("p", { class: "leaves" }, "No server is connected. Nothing can leave this computer until you connect one."),
      h("button", { type: "button", class: "primary", onclick: () => app.go("setup") }, "Connect to your server"));
    return;
  }
  put(main, 
    h("p", null, h("code", null, info.URL)),
    h("p", { class: info.Healthy ? "good" : "bad" }, info.Healthy ? "● The server answers." : "✕ The server cannot be reached."),
    h("p", { class: info.TokenOK ? "good" : "bad" }, info.TokenOK ? "● This computer's upload token is accepted." : "✕ This computer's upload token was refused."),
    info.TokenStore ? h("p", { class: "dim" }, `The upload token is kept in the ${info.TokenStore}.`) : null,
    info.Err ? h("p", { class: "bad" }, info.Err) : null,
    h("h2", null, "What the server holds"),
    h("p", { class: "leaves" }, "This is everything of yours on the server. Deleting a project removes it for every assistant at once; files on this computer are not touched."));
  const projects = info.Projects || [];
  if (!projects.length) {
    put(main, h("p", { class: "dim" }, "Nothing yet."));
    return;
  }
  put(main, h("table", null,
    h("thead", null, h("tr", null, h("th", null, "Project"), h("th", { class: "num" }, "Sessions"), h("th", null, "Last activity"), h("th", null, ""))),
    h("tbody", null, projects.map((p) => h("tr", null,
      h("td", null, p.Label), h("td", { class: "num" }, String(p.Sessions)), h("td", null, when(p.Last)),
      h("td", null, h("button", {
        type: "button",
        onclick: async () => {
          if (!await ask(`Delete ${p.Label} from the server?`, [`${plural(p.Sessions, "session")} will be deleted from the server.`, "If the project is still shared, the next send uploads it again."], "Delete from the server")) return;
          try { await api("/api/server/delete-project", { Ref: p.ID || p.Label }); app.say(`Deleted ${p.Label} from the server.`); } catch (e) { app.say(String(e.message || e), true); }
          render(main, app);
        },
      }, "Delete")))))));
}
