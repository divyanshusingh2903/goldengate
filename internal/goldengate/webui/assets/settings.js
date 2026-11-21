// Settings: how much is sent, from which tools, how far back, what else is
// redacted, and who writes the summaries.

import { api } from "/assets/api.js";
import { h, plural, put, fill, ask, head, card, cardBody, note } from "/assets/dom.js";

const PROVIDERS = [
  ["", "Local rules (nothing is sent to a model)"],
  ["anthropic", "Anthropic"], ["openai", "OpenAI"], ["gemini", "Gemini"],
  ["opencode", "OpenCode Zen"], ["ollama", "Ollama (on this computer)"], ["custom", "Custom OpenAI-compatible endpoint"],
];

// keyText says where a provider's API key is, as the backend reports it.
function keyText(src, name) {
  if (!src || src === "none") return `No key is stored for ${name}.`;
  if (src === "file") return `A key for ${name} is stored in a file on this computer that only you can read but that is not encrypted (there is no keyring).`;
  if (src === "keyring") return `A key for ${name} is stored in this computer's keyring.`;
  if (src.startsWith("$")) return `${name} uses the key in the environment variable ${src}.`;
  return `A key for ${name} is stored (${src}).`;
}

function select(id, options, value) {
  return h("select", { id }, options.map(([v, label]) => h("option", { value: v, selected: v === value }, label)));
}

export async function render(main, app) {
  const s = await api("/api/settings");
  const sum = s.Summarize || {};
  const provider0 = sum.Provider === "heuristic" ? "" : (sum.Provider || "");

  const level = select("set-level", [["summary", "Summary: title, goals, outcomes, files and work items"], ["titles-only", "Titles only: the session title and work items"]], s.Level);
  const since = h("input", { type: "text", id: "set-since", value: s.Since || "" });
  const patterns = h("textarea", { id: "set-patterns" }, (s.ExtraPatterns || []).join("\n"));
  const git = h("input", { type: "checkbox", class: "switch", checked: !!s.EnrichGit, "aria-label": "Read git metadata" });
  const sources = (s.Sources || []).map((src) => ({ src, box: h("input", { type: "checkbox", class: "switch", checked: !!src.Enabled, "aria-label": `Read ${src.Name}` }) }));

  const provider = select("set-provider", PROVIDERS, provider0);
  const baseURL = h("input", { type: "url", id: "set-base", value: sum.BaseURL || "", placeholder: "leave empty for the provider's own address" });
  const length = select("set-length", [["short", "Short: one paragraph"], ["", "Standard: two to four paragraphs"], ["detailed", "Detailed: several paragraphs"]], sum.Length === "standard" ? "" : (sum.Length || ""));
  const modelNote = h("p", { class: "note" });
  const modelFields = h("div", null);
  const keyBox = h("div", { class: "field" });
  const modelBox = h("div", { class: "field gap-top" });
  const urlBox = h("div", null);

  // What the backend knows about every provider, keys included; refreshed
  // after a key is stored or removed.
  let infos = sum.Providers || [];
  const info = () => infos.find((p) => p.ID === provider.value) || { ID: provider.value, KeySource: "none" };
  // the provider's name without the note in brackets: "Ollama", not "Ollama (on this computer)"
  const label = () => (PROVIDERS.find(([v]) => v === provider.value) || [, provider.value])[1].replace(/ \(.*\)$/, "");
  const hasKey = () => { const k = info().KeySource; return !!k && k !== "none"; };
  // the model each provider starts with: the saved one for the saved provider
  const models0 = { [provider0]: sum.Model || "" };
  let model = sum.Model || "";
  let typed = false; // the user chose to type a model name

  const reloadKeys = async () => { infos = ((await api("/api/settings")).Summarize || {}).Providers || infos; };

  const drawKey = (replacing) => {
    const name = label();
    const input = h("input", { type: "password", id: "set-key", autocomplete: "off", placeholder: `paste your ${name} API key` });
    const store = async () => {
      if (!input.value) { app.say("Paste a key first.", true); return; }
      try {
        await api("/api/summarizer/key", { Provider: provider.value, Key: input.value });
        input.value = "";
        await reloadKeys();
        app.say(`Key stored for ${name}.`);
        drawKey(false);
        loadModels();
      } catch (e) { app.say(String(e.message || e), true); }
    };
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); store(); } });
    const src = info().KeySource;
    const stored = hasKey() && !String(src).startsWith("$");
    let body;
    if (hasKey() && !replacing) {
      body = [h("p", { class: "key-state good" }, "✓ ", keyText(src, name)),
        h("div", { class: "row" },
          h("button", { type: "button", onclick: () => { drawKey(true); document.getElementById("set-key").focus(); } }, stored ? "Replace key" : "Store a key instead"),
          stored ? h("button", {
            type: "button", class: "danger",
            onclick: async () => {
              if (!await ask(`Remove the ${name} key?`, ["It is deleted from this computer. Summaries by this provider stop working until a key is stored again."], "Remove key")) return;
              try { await api("/api/summarizer/key", { Provider: provider.value, Key: "" }); await reloadKeys(); app.say("Key removed."); drawKey(false); loadModels(); } catch (e) { app.say(String(e.message || e), true); }
            },
          }, "Remove key") : null)];
    } else {
      body = [
        info().KeyOptional && !hasKey() ? h("p", { class: "hint" }, "A server on this computer usually needs no key. Store one only if yours asks for it.") : null,
        h("div", { class: "row" }, h("span", { class: "grow-field" }, input),
          h("button", { type: "button", class: hasKey() || info().KeyOptional ? "" : "primary", onclick: store }, "Store key"),
          replacing ? h("button", { type: "button", class: "ghost", onclick: () => drawKey(false) }, "Cancel") : null),
        h("p", { class: "hint" }, "It stays on this computer: in its keyring, or in a file only you can read when there is no keyring. It is never shown again and never sent to your server.")];
    }
    fill(keyBox, h("label", { for: "set-key" }, "API key"), body);
  };

  const drawModels = (list, state) => {
    const items = [...new Set([...(list || [])])];
    if (model && !items.includes(model)) items.unshift(model);
    const lbl = h("label", { for: "set-model" }, "Model");
    if (state === "wait") { fill(modelBox, lbl, h("p", { class: "hint" }, hasKey() || info().KeyOptional ? "Loading the models…" : "Store a key above and the models load here.")); return; }
    const text = h("input", { type: "text", id: "set-model", value: model, autocomplete: "off", placeholder: info().DefaultModel ? `for example ${info().DefaultModel}` : "the model's name" });
    text.addEventListener("input", () => { model = text.value.trim(); });
    if (typed || !items.length) {
      fill(modelBox, lbl, text,
        state ? h("p", { class: "hint bad" }, state) : null,
        h("div", { class: "row" },
          (list || []).length ? h("button", { type: "button", class: "sm", onclick: () => { typed = false; drawModels(list); } }, "Choose from the list") : null,
          h("button", { type: "button", class: "sm", onclick: () => loadModels() }, "Load the models again")));
      return;
    }
    const sel = h("select", { id: "set-model" }, items.map((m) => h("option", { value: m, selected: m === model }, m === info().DefaultModel ? `${m} (default)` : m)));
    sel.addEventListener("change", () => { model = sel.value; });
    model = sel.value;
    fill(modelBox, lbl, sel,
      h("p", { class: "hint" }, `${plural(list.length, "model")} offered by ${label()}. `,
        h("button", { type: "button", class: "link", onclick: () => { typed = true; drawModels(list); } }, "Type a name instead"), " · ",
        h("button", { type: "button", class: "link", onclick: () => loadModels() }, "Load again")));
  };

  let seq = 0; // only the newest load draws
  const loadModels = async () => {
    const me = ++seq;
    if (!hasKey() && !info().KeyOptional) { drawModels([], "wait"); return; }
    drawModels([], "wait");
    try {
      const list = await api(`/api/summarizer/models?provider=${encodeURIComponent(provider.value)}&base_url=${encodeURIComponent(baseURL.value.trim())}`);
      if (me !== seq) return;
      const got = (list || []).slice().sort();
      // start from the provider's default when it offers it, else its first
      if (!model) model = got.includes(info().DefaultModel) ? info().DefaultModel : (got[0] || "");
      drawModels(got);
    } catch (e) {
      if (me !== seq) return;
      typed = true;
      drawModels([], `The models could not be listed: ${e.message || e}. Type the model's name.`);
    }
  };

  const drawURL = () => {
    const local = provider.value === "ollama" || provider.value === "custom";
    const def = info().DefaultURL;
    baseURL.placeholder = def ? `leave empty for ${def}` : "leave empty for the provider's own address";
    const field = h("div", { class: "field gap-top" }, h("label", { for: "set-base" }, "Endpoint address"), baseURL);
    fill(urlBox, local || baseURL.value ? field : h("details", { class: "more gap-top" }, h("summary", null, "Use a different endpoint address"), field));
  };
  baseURL.addEventListener("change", () => loadModels());

  const describe = () => {
    const on = provider.value !== "";
    modelFields.hidden = !on;
    modelNote.textContent = on
      ? "With a model on, the redacted conversation of each session you send goes to that provider so it can write the summary. Sessions that wait for your approval are never sent to it."
      : "Summaries are written by rules on this computer. Nothing is sent to any model.";
  };
  let shown = provider0;
  provider.addEventListener("change", () => {
    models0[shown] = model;
    shown = provider.value;
    model = models0[shown] ?? "";
    typed = false;
    if (provider.value !== provider0) baseURL.value = ""; // an address belongs to one provider
    else baseURL.value = sum.BaseURL || "";
    describe();
    if (!provider.value) return;
    drawKey(false); drawURL(); loadModels();
  });

  const collect = () => ({
    Level: level.value, Since: since.value.trim(),
    Sources: sources.map(({ src, box }) => ({ ...src, Enabled: box.checked })),
    ExtraPatterns: patterns.value.split("\n").map((l) => l.trim()).filter(Boolean),
    EnrichGit: git.checked, Theme: s.Theme || "",
    Summarize: { Provider: provider.value, Model: provider.value ? model.trim() : "", BaseURL: baseURL.value.trim(), Length: length.value, KeySource: sum.KeySource || "" },
  });
  const save = async () => {
    try { await api("/api/settings", collect()); app.say("Saved."); await app.refresh(); return true; } catch (e) { app.say(String(e.message || e), true); return false; }
  };

  put(modelFields, keyBox, modelBox, urlBox,
    h("div", { class: "field gap-top" }, h("label", { for: "set-length" }, "Narrative length"), length),
    h("div", { class: "row gap-top" },
      h("button", {
        type: "button",
        onclick: async () => {
          if (!await save()) return;
          app.say("Testing with a tiny sample (not your data)…");
          try { app.say((await api("/api/summarizer/test", {})).Message); } catch (e) { app.say(String(e.message || e), true); }
        },
      }, "Save and test")));

  // Background sending is not part of the form: it installs or removes a
  // service, so it happens at once and says if it could not.
  const d = s.Daemon || {};
  const bg = h("input", { type: "checkbox", class: "switch", checked: d.Mode === "on", "aria-label": "Send in the background" });
  bg.addEventListener("change", async () => {
    bg.disabled = true;
    const want = bg.checked;
    try {
      const r = await api("/api/daemon", { On: want });
      app.say(want ? (r.warning ? "Background sending is on, but " + r.warning.split("\n")[0] : "Background sending is on.") : "Background sending is off.", !!(want && r.warning));
      await app.go("settings");
    } catch (e) {
      bg.checked = !want;
      bg.disabled = false;
      app.say(String(e.message || e), true);
    }
  });
  const bgState = d.LastError && d.Mode === "on" ? "Problem: " + d.LastError
    : d.Mode === "on" && d.Running ? "Running. " + (d.LastResult || "Nothing sent yet.")
      : d.Mode === "on" ? "Turned on, but no background process is running: run goldengate daemon status."
        : d.Ask ? "A project already sends on its own: turn this on so you do not have to remember to send."
          : "Not running.";
  const row = (title, desc, control) => h("div", { class: "set-row" }, h("div", null, h("div", { class: "t" }, title), desc ? h("div", { class: "d" }, desc) : null), control);
  fill(main,
    h("form", { onsubmit: (e) => { e.preventDefault(); save(); } },
      head("Settings", "What is read, what is sent, and who writes the summaries.", h("button", { type: "submit", class: "primary" }, "Save settings")),
      h("div", { class: "grid" },
        card("What is sent", null,
          row(h("label", { for: "set-level" }, "How much of each session"), null, level),
          row(h("label", { for: "set-since" }, "How far back"), "For example 90d. Older sessions are not sent.", since)),
        card("Tools", null,
          h("p", { class: "card-body hint" }, "A tool that is off is never read for sending, whatever is shared."),
          sources.map(({ src, box }) => row(src.Name, src.Note || (src.Installed ? "" : "Not found on this computer"), box))),
        card("Redaction", null,
          cardBody(h("div", { class: "field" }, h("label", { for: "set-patterns" }, "Extra patterns to redact, one per line"), patterns,
            h("p", { class: "hint" }, "Secrets, keys and home paths are always redacted. Add regular expressions for anything else, such as a client's name."))),
          row("Read git metadata", "Each folder's HEAD and config, to recognise worktrees, remotes and branches.", git)),
        card("Send in the background", null,
          h("p", { class: "card-body hint" }, "A small background process sends what you or your rules approved, so nothing depends on remembering to send. It never sends what waits in the inbox, and every send is listed in Receipts."),
          row("Send automatically", bgState, bg)),
        card("Who writes the summaries", null,
          cardBody(h("div", { class: "field" }, h("label", { for: "set-provider" }, "Summaries by"), provider), modelNote, h("div", { class: "gap-top" }, modelFields))))),
  );
  describe();
  if (provider.value) { drawKey(false); drawURL(); loadModels(); }
}
