// Setup: find the tools on this computer and connect to your server.

import { api } from "/assets/api.js";
import { h, clear, put, fill, head, card, cardBody, table, docs, errText } from "/assets/dom.js";

export async function render(main, app) {
  const [tools, snap] = await Promise.all([api("/api/detect"), app.refresh()]);
  const url = h("input", { type: "url", id: "srv-url", value: snap.Server || "", placeholder: "the address of your server", autocomplete: "off" });
  const admin = h("input", { type: "password", id: "srv-admin", autocomplete: "off" });
  const pair = h("input", { type: "text", id: "srv-pair", autocomplete: "off" });
  const result = h("div", null);
  const connect = h("button", { type: "submit", class: "primary" }, "Connect");

  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      connect.disabled = true;
      app.say("Connecting…", "busy");
      try {
        const r = await api("/api/init", { ServerURL: url.value.trim(), AdminToken: admin.value, PairCode: pair.value.trim() });
        admin.value = ""; // it was used for this one call and is not kept anywhere
        pair.value = "";
        await app.refresh();
        app.say("Connected.");
        showResult(result, r, app);
      } catch (err) {
        app.say(errText(err), true);
      }
      connect.disabled = false;
    },
  },
  h("div", { class: "field" }, h("label", { for: "srv-url" }, "Server address"), url),
  h("div", { class: "field" }, h("label", { for: "srv-admin" }, "Admin secret"), admin,
    h("p", { class: "hint" }, "Used once, never stored.")),
  h("div", { class: "field" }, h("label", { for: "srv-pair" }, "Or a pairing code"), pair,
    h("p", { class: "hint" }, "From `goldengatebridge pair` on the server.")),
  h("div", { class: "row" }, connect));

  const tools_ = card("Tools found on this computer", null, table(["Tool", "Found", h("th", { class: "num" }, "Sessions"), "Note"],
    (tools || []).map((t) => h("tr", null,
      h("td", null, t.Name), h("td", { class: t.Installed ? "w good" : "w dim" }, t.Installed ? "✓ yes" : "– no"),
      h("td", { class: "num" }, t.Installed ? String(t.Sessions) : ""), h("td", { class: "dim small" }, t.Note || "")))));
  const formCard = (title) => card(title, null, cardBody(form, result));

  if (!snap.Server) {
    fill(main,
      head("Setup", "Find the tools on this computer and connect to your server.", docs("start#setup")),
      h("div", { class: "grid setup" }, tools_, formCard("Connect to your server")));
    return;
  }

  // Connected: lead with the server; the form is there only on request.
  const status = h("dd", { class: "dim" }, "checking…");
  const token = h("dd", { class: "dim" }, "checking…");
  const mcp = snap.Server.replace(/\/+$/, "") + "/mcp";
  const copied = h("span", { class: "hint" });
  const other = h("div", { hidden: true }, formCard("Connect to a different server"));
  other.firstChild.classList.add("gap-top");
  const change = h("button", {
    type: "button",
    onclick: () => { other.hidden = !other.hidden; change.textContent = other.hidden ? "Connect to a different server" : "Keep this server"; if (!other.hidden) url.focus(); },
  }, "Connect to a different server");
  const connected = card("Your server", h("span", { class: "badge good" }, "● Connected"), cardBody(
    h("dl", { class: "kv" },
      h("dt", null, "Address"), h("dd", { class: "mono" }, snap.Server),
      h("dt", null, "Status"), status,
      h("dt", null, "Upload token"), token,
      h("dt", null, "Connector"), h("dd", null, h("code", null, mcp), " ",
        h("button", { type: "button", class: "sm", onclick: async () => { try { await navigator.clipboard.writeText(mcp); copied.textContent = "copied"; } catch { copied.textContent = "select the text and copy it"; } } }, "Copy"), " ", copied)),
    h("div", { class: "row gap-top" },
      h("button", { type: "button", onclick: () => app.go("agents") }, "Agents"),
      h("button", { type: "button", onclick: () => app.go("server") }, "What the server holds"),
      change)));

  fill(main,
    head("Setup", "This computer is connected. Choose what to share next.", docs("start#setup"),
      h("button", { type: "button", class: "primary", onclick: () => app.go("projects") }, "Next: choose what to share →")),
    h("div", { class: "grid setup" }, tools_, h("div", null, connected, other)));

  try {
    const info = await api("/api/server");
    fill(status, info.Healthy ? h("span", { class: "good" }, "● answers") : h("span", { class: "bad" }, "✕ unreachable", info.Err ? h("small", { class: "dim" }, ` (${info.Err})`) : null));
    fill(token, info.TokenOK ? h("span", { class: "good" }, "● accepted", info.TokenStore ? h("span", { class: "dim" }, ` · kept in the ${info.TokenStore}`) : null) : h("span", { class: "bad" }, "✕ refused: connect again to make a new one"));
    status.className = token.className = "";
  } catch (e) {
    fill(status, h("span", { class: "bad" }, errText(e)));
    fill(token, "unknown");
  }
}

function showResult(box, r, app) {
  clear(box);
  if (r.ReadToken) {
    const copied = h("span", { class: "dim" });
    put(box, h("div", { class: "once" },
      h("h2", null, "Your assistant's token (shown once)"),
      h("p", null, "Give this to your assistant's connector. It cannot be shown again; leaving this screen discards it."),
      h("code", null, r.ReadToken),
      h("div", { class: "row" },
        h("button", { type: "button", onclick: async () => { try { await navigator.clipboard.writeText(r.ReadToken); copied.textContent = "copied"; } catch { copied.textContent = "select the text and copy it"; } } }, "Copy"), copied),
      h("p", null, "Connector address: ", h("code", null, r.MCPURL || "")),
      r.SeesAll ? h("p", { class: "dim" }, "This first assistant can see every project you share. Assistants you add later start with nothing.") : null));
  } else {
    put(box, h("p", null, "Connected. Connector address for your assistant: ", h("code", null, r.MCPURL || "")));
  }
  if (r.TokenStore) put(box, h("p", { class: "dim" }, `This computer's upload token is stored in the ${r.TokenStore}.`));
  app.say("Connected. The admin secret was not saved.");
}
