// Setup: find the tools on this computer and connect to your server.

import { api } from "/assets/api.js";
import { h, clear, plural, put, fill } from "/assets/dom.js";

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
      app.say("Connecting…");
      try {
        const r = await api("/api/init", { ServerURL: url.value.trim(), AdminToken: admin.value, PairCode: pair.value.trim() });
        admin.value = ""; // it was used for this one call and is not kept anywhere
        pair.value = "";
        await app.refresh();
        app.say("Connected.");
        showResult(result, r, app);
      } catch (err) {
        app.say(String(err.message || err), true);
      }
      connect.disabled = false;
    },
  },
  h("label", { for: "srv-url" }, "Server address"), url,
  h("label", { for: "srv-admin" }, "Admin secret"), admin,
  h("p", { class: "dim" }, "Used once to create this computer's upload token and your assistant's token. It is not stored, here or on disk."),
  h("label", { for: "srv-pair" }, "Or a pairing code"), pair,
  h("p", { class: "dim" }, "From `goldengatebridge pair` on the server, when you would rather not type the admin secret here."),
  h("div", { class: "row" }, connect));

  fill(main, 
    h("h1", null, "Setup"),
    h("p", { class: "leaves" }, "Nothing leaves this computer during setup. Connecting only creates tokens; you choose what is shared on the Projects screen, and it is sent only when you press Send."),
    h("h2", null, "Tools found on this computer"),
    h("table", null,
      h("thead", null, h("tr", null, h("th", null, "Tool"), h("th", null, "Found"), h("th", { class: "num" }, "Sessions"), h("th", null, "Note"))),
      h("tbody", null, (tools || []).map((t) => h("tr", null,
        h("td", null, t.Name), h("td", null, t.Installed ? "✓ yes" : "– no"), h("td", { class: "num" }, t.Installed ? String(t.Sessions) : ""), h("td", { class: "dim" }, t.Note || ""))))),
    h("h2", null, snap.Server ? "Your server" : "Connect to your server"),
    snap.Server ? h("p", null, "Connected to ", h("code", null, snap.Server), ". Fill the form again only to connect to a different server or to make new tokens.") : null,
    form, result,
    h("div", { class: "row" }, h("button", { type: "button", onclick: () => app.go("projects") }, "Next: choose what to share →")),
  );
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
