// Settings: how much is sent, from which tools, how far back, what else is
// redacted, and who writes the summaries.

import { api } from "/assets/api.js";
import { h, clear, plural, put, fill } from "/assets/dom.js";

const PROVIDERS = [
  ["", "Local rules (nothing is sent to a model)"],
  ["anthropic", "Anthropic"], ["openai", "OpenAI"], ["gemini", "Gemini"],
  ["opencode", "OpenCode Zen"], ["ollama", "Ollama (on this computer)"], ["custom", "Custom OpenAI-compatible endpoint"],
];

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
  const git = h("input", { type: "checkbox", checked: !!s.EnrichGit });
  const sources = (s.Sources || []).map((src) => ({ src, box: h("input", { type: "checkbox", checked: !!src.Enabled }) }));

  const provider = select("set-provider", PROVIDERS, provider0);
  const model = h("input", { type: "text", id: "set-model", value: sum.Model || "", list: "set-models", autocomplete: "off" });
  const models = h("datalist", { id: "set-models" });
  const baseURL = h("input", { type: "url", id: "set-base", value: sum.BaseURL || "", placeholder: "leave empty for the provider's own address" });
  const length = select("set-length", [["short", "Short: one paragraph"], ["", "Standard: two to four paragraphs"], ["detailed", "Detailed: several paragraphs"]], sum.Length === "standard" ? "" : (sum.Length || ""));
  const key = h("input", { type: "password", id: "set-key", autocomplete: "off", placeholder: "paste a key to store it" });
  const keyState = h("span", { class: "dim" }, sum.KeySource && sum.KeySource !== "none" ? `A key is set (${sum.KeySource}).` : "No key is set.");
  const modelNote = h("p", { class: "leaves" });
  const modelFields = h("div", null);

  const describe = () => {
    const on = provider.value !== "";
    modelFields.hidden = !on;
    modelNote.textContent = on
      ? "With a model on, the redacted conversation of each session you send goes to that provider so it can write the summary. Sessions that wait for your approval are never sent to it."
      : "Summaries are written by rules on this computer. Nothing is sent to any model.";
  };
  provider.addEventListener("change", describe);

  const collect = () => ({
    Level: level.value, Since: since.value.trim(),
    Sources: sources.map(({ src, box }) => ({ ...src, Enabled: box.checked })),
    ExtraPatterns: patterns.value.split("\n").map((l) => l.trim()).filter(Boolean),
    EnrichGit: git.checked, Theme: s.Theme || "",
    Summarize: { Provider: provider.value, Model: model.value.trim(), BaseURL: baseURL.value.trim(), Length: length.value, KeySource: sum.KeySource || "" },
  });
  const save = async () => {
    try { await api("/api/settings", collect()); app.say("Saved."); await app.refresh(); return true; } catch (e) { app.say(String(e.message || e), true); return false; }
  };

  put(modelFields, 
    h("label", { for: "set-model" }, "Model"), model, models,
    h("div", { class: "row" }, h("button", {
      type: "button",
      onclick: async () => {
        try {
          const list = await api(`/api/summarizer/models?provider=${encodeURIComponent(provider.value)}&base_url=${encodeURIComponent(baseURL.value.trim())}`);
          clear(models);
          for (const m of list || []) put(models, h("option", { value: m }));
          app.say(`${plural((list || []).length, "model")} offered; start typing in the Model field.`);
        } catch (e) { app.say(String(e.message || e), true); }
      },
    }, "List this provider's models")),
    h("label", { for: "set-base" }, "Endpoint address"), baseURL,
    h("label", { for: "set-length" }, "Narrative length"), length,
    h("label", { for: "set-key" }, "API key"), key,
    h("p", { class: "dim" }, "A key you store goes into this computer's keyring. It is never shown again and never sent to your server. ", keyState),
    h("div", { class: "row" },
      h("button", {
        type: "button",
        onclick: async () => {
          if (!key.value) { app.say("Paste a key first.", true); return; }
          try { await api("/api/summarizer/key", { Provider: provider.value, Key: key.value }); key.value = ""; keyState.textContent = "A key is set (keyring)."; app.say("Key stored."); } catch (e) { app.say(String(e.message || e), true); }
        },
      }, "Store key"),
      h("button", {
        type: "button",
        onclick: async () => {
          try { await api("/api/summarizer/key", { Provider: provider.value, Key: "" }); keyState.textContent = "No key is set."; app.say("Key removed."); } catch (e) { app.say(String(e.message || e), true); }
        },
      }, "Remove key"),
      h("button", {
        type: "button",
        onclick: async () => {
          if (!await save()) return;
          app.say("Testing with a tiny sample (not your data)…");
          try { app.say((await api("/api/summarizer/test", {})).Message); } catch (e) { app.say(String(e.message || e), true); }
        },
      }, "Save and test")));

  fill(main, 
    h("h1", null, "Settings"),
    h("form", { onsubmit: (e) => { e.preventDefault(); save(); } },
      h("h2", null, "What is sent"),
      h("label", { for: "set-level" }, "How much of each session"), level,
      h("label", { for: "set-since" }, "How far back"), since,
      h("p", { class: "dim" }, "For example 90d. Older sessions are not sent."),
      h("h2", null, "Tools"),
      h("p", { class: "dim" }, "A tool that is off is never read for sending, whatever is shared."),
      sources.map(({ src, box }) => h("label", { class: "check" }, box,
        `${src.Name}${src.Installed ? "" : " (not found on this computer)"}${src.Note ? " · " + src.Note : ""}`)),
      h("h2", null, "Redaction"),
      h("label", { for: "set-patterns" }, "Extra patterns to redact, one per line"), patterns,
      h("p", { class: "dim" }, "Secrets, keys and home paths are always redacted. Add regular expressions for anything else, such as a client's name."),
      h("label", { class: "check" }, git, "Read each folder's git metadata (HEAD and config) to recognise worktrees, remotes and branches"),
      h("h2", null, "Who writes the summaries"),
      h("label", { for: "set-provider" }, "Summaries by"), provider,
      modelNote, modelFields,
      h("div", { class: "row" }, h("button", { type: "submit", class: "primary" }, "Save settings"))),
  );
  describe();
}
