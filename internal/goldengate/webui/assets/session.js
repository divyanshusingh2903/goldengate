// A session as it would leave this computer: its summary to read, and the
// exact JSON a tab away. Used by Preview and by the send review. Everything
// is built as DOM nodes from the payload, never as HTML.

import { h, fill, marked, plural, KINDS, workIcon, workCounts, stamp } from "/assets/dom.js";

const text = (s) => marked(String(s == null ? "" : s));

// inline renders the little markdown a narrative uses: **bold** and `code`.
function inline(s) {
  const out = [];
  for (const part of String(s).split(/(\*\*[^*\n]+\*\*|`[^`\n]+`)/)) {
    if (!part) continue;
    if (part.startsWith("**") && part.endsWith("**")) out.push(h("strong", null, text(part.slice(2, -2))));
    else if (part.startsWith("`") && part.endsWith("`")) out.push(h("code", null, text(part.slice(1, -1))));
    else out.push(...text(part));
  }
  return out;
}

// prose turns a narrative into paragraphs and bullet lists.
function prose(md) {
  const out = [];
  for (const block of String(md).split(/\n{2,}/)) {
    const lines = block.split("\n").map((l) => l.trim()).filter(Boolean);
    if (!lines.length) continue;
    if (lines.every((l) => /^[-*] /.test(l))) {
      out.push(h("ul", null, lines.map((l) => h("li", null, inline(l.slice(2))))));
    } else {
      out.push(h("p", null, inline(lines.join(" ").replace(/^#+\s*/, ""))));
    }
  }
  return out;
}

function section(title, ...body) {
  const kids = body.flat().filter(Boolean);
  return kids.length ? h("section", { class: "sv-sec" }, h("h4", null, title), kids) : null;
}

function bullets(items) {
  return items && items.length ? h("ul", null, items.map((x) => h("li", null, inline(x)))) : null;
}

function duration(sec) {
  if (!sec) return "";
  const m = Math.round(sec / 60);
  if (m < 1) return "under a minute";
  if (m < 60) return `${m} min`;
  return `${Math.floor(m / 60)} h ${m % 60 ? (m % 60) + " min" : ""}`.trim();
}

const STATUS = { created: "● created", updated: "● updated", merged: "✓ merged", completed: "✓ completed", closed: "✓ closed", canceled: "– canceled", mentioned: "mentioned" };

// SHOWN is how many items of a kind are listed before "Show all".
const SHOWN = 8;

// workGroups lists the work items by kind, each kind under its icon and
// count; a long kind folds after its first few items.
function workGroups(items) {
  // Keys from the session's main repository read as "#12" or a short SHA;
  // the full key is on hover.
  const repoOf = (k) => (/^(?:[a-z]+:)?([^#@!]+\/[^#@!]+)[#@!]/.exec(k) || [])[1] || "";
  const seen = {};
  for (const w of items) { const r = repoOf(w.key); if (r) seen[r] = (seen[r] || 0) + 1; }
  const main = Object.entries(seen).sort((a, b) => b[1] - a[1]).map(([r]) => r)[0] || "";
  const short = (k) => {
    const m = /^(?:[a-z]+:)?([^#@!]+\/[^#@!]+)([#@!])(.+)$/.exec(k);
    if (!m || m[1] !== main) return k;
    return m[2] === "@" ? m[3].slice(0, 7) : m[2] + m[3];
  };
  const line = (w) => h("li", null, workIcon(w.kind), h("code", { title: w.key }, text(short(w.key))), w.title && w.kind !== "file" ? [" ", text(w.title)] : null,
    w.status ? h("span", { class: "faint" }, ` · ${STATUS[w.status] || w.status}`) : null);
  return KINDS.map(([kind, one, many]) => {
    const its = items.filter((w) => w.kind === kind);
    if (!its.length) return null;
    const list = (xs) => h("ul", { class: "sv-work" }, xs.map(line));
    return h("div", { class: "sv-kind" },
      h("h5", null, workIcon(kind), plural(its.length, one, many)),
      list(its.slice(0, SHOWN)),
      its.length > SHOWN ? h("details", null, h("summary", null, `Show ${its.length - SHOWN} more`), list(its.slice(SHOWN))) : null);
  });
}

// summaryView shows the payload the way an assistant will read it.
export function summaryView(p) {
  const s = p.summary || {};
  const goals = (s.goals && s.goals.length) ? s.goals : (s.goal ? [s.goal] : []);
  const outcomes = (s.outcomes && s.outcomes.length) ? s.outcomes : (s.outcome ? [s.outcome] : []);
  const facts = [
    stamp(p.updated_at) ? ["Updated", stamp(p.updated_at)] : null,
    s.git_branch ? ["Branch", h("code", null, s.git_branch)] : null,
    s.turn_count ? ["Turns", String(s.turn_count)] : null,
    s.duration_sec ? ["Length", duration(s.duration_sec)] : null,
    ["Level", p.level || "summary"],
    s.summarized_by ? ["Written by", s.summarized_by === "heuristic" ? "local rules (no model)" : s.summarized_by] : null,
    s.edited ? ["Edited", "by you"] : null,
  ].filter(Boolean);
  const reds = Object.entries(p.redactions || {}).filter(([, n]) => n > 0);
  const files = s.files_touched || [];
  const tools = Object.entries(s.tools_used || {}).sort((a, b) => b[1] - a[1]);

  return h("div", { class: "sv" },
    h("h3", { class: "sv-title" }, text(s.title || p.title_only || "(untitled)")),
    s.abstract ? h("p", { class: "sv-abstract" }, inline(s.abstract)) : null,
    p.summary ? h("p", { class: "sv-counts" }, workCounts(p.work_items)) : null,
    h("div", { class: "sv-facts" }, facts.map(([k, v]) => h("div", null, h("span", { class: "k" }, k), h("span", { class: "v" }, v)))),
    reds.length ? h("p", { class: "sv-red" }, "Redacted before it leaves: ", reds.map(([k, n], i) => [i ? ", " : "", h("mark", null, `${k} ×${n}`)])) : null,
    !p.summary ? h("p", { class: "hint" }, "Only the title is sent at this share level.") : null,
    s.narrative ? section(s.narrative_by === "user" ? "Your summary" : "Summary", prose(s.narrative)) : null,
    section("Asked for", bullets(goals)),
    section("What was done", bullets(s.description)),
    section("Outcomes", bullets(outcomes)),
    section("Left open", bullets(s.open_problems)),
    (p.work_items || []).length ? section(`Work items (${p.work_items.length})`, workGroups(p.work_items)) : null,
    files.length ? section(`Files touched (${files.length})`,
      files.length > 8
        ? h("details", null, h("summary", null, `Show all ${files.length}`), h("ul", { class: "sv-files" }, files.map((f) => h("li", null, h("code", null, text(f))))))
        : h("ul", { class: "sv-files" }, files.map((f) => h("li", null, h("code", null, text(f)))))) : null,
    tools.length ? section("Tools used", h("div", { class: "chips" }, tools.map(([k, n]) => h("span", { class: "chip" }, k, h("span", { class: "faint" }, ` ×${n}`))))) : null,
    (s.commands || []).length ? section("Commands", h("div", { class: "chips" }, s.commands.map((c) => h("span", { class: "chip mono" }, c)))) : null,
    s.first_message ? section("First message", h("blockquote", null, inline(s.first_message))) : null,
    s.last_message ? section("Last reply", h("blockquote", null, inline(s.last_message))) : null);
}

// jsonTree draws a value as a tree whose objects and arrays fold.
export function jsonTree(value) {
  const leaf = (v) => {
    if (v === null) return h("span", { class: "j-null" }, "null");
    if (typeof v === "string") return h("span", { class: "j-str" }, "\"", text(v), "\"");
    if (typeof v === "number") return h("span", { class: "j-num" }, String(v));
    if (typeof v === "boolean") return h("span", { class: "j-bool" }, String(v));
    return h("span", null, String(v));
  };
  const node = (key, v, depth, last) => {
    const k = key === null ? null : h("span", { class: "j-key" }, typeof key === "number" ? "" : `"${key}"`, typeof key === "number" ? "" : ": ");
    const comma = last ? "" : ",";
    const isArr = Array.isArray(v);
    if (v && typeof v === "object") {
      const entries = isArr ? v.map((x, i) => [i, x]) : Object.entries(v);
      const [open, close] = isArr ? ["[", "]"] : ["{", "}"];
      if (!entries.length) return h("div", { class: "j-row" }, k, `${open}${close}${comma}`);
      const size = isArr ? plural(entries.length, "item") : plural(entries.length, "key");
      return h("details", { class: "j-node", open: depth < 2 },
        h("summary", null, k, open, h("span", { class: "j-size" }, ` ${size} `), h("span", { class: "j-close" }, `${close}${comma}`)),
        h("div", { class: "j-kids" }, entries.map(([ck, cv], i) => node(ck, cv, depth + 1, i === entries.length - 1))),
        h("div", { class: "j-row" }, close + comma));
    }
    return h("div", { class: "j-row" }, k, leaf(v), comma);
  };
  return h("div", { class: "jt" }, node(null, value, 0, true));
}

// sessionView is a session's summary, with the exact JSON one tab away.
// json is the text that would go on the wire; the tree is drawn from it.
export function sessionView(payload, json) {
  const body = h("div", { class: "sv-body" });
  const tabs = h("div", { class: "tabs sv-tabs", role: "tablist", "aria-label": "Show" });
  let tree = null;
  const show = (which) => {
    for (const b of tabs.children) b.setAttribute("aria-selected", String(b.dataset.tab === which));
    if (which === "summary") { fill(body, summaryView(payload)); return; }
    let value = payload;
    try { value = JSON.parse(json); } catch { /* the payload itself */ }
    tree = jsonTree(value);
    const all = (open) => { for (const d of tree.querySelectorAll("details")) d.open = open; };
    const copy = h("button", { type: "button", class: "sm" }, "Copy JSON");
    copy.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(json || JSON.stringify(payload, null, 2)); copy.textContent = "Copied"; } catch { copy.textContent = "Copy failed"; }
      setTimeout(() => { copy.textContent = "Copy JSON"; }, 1500);
    });
    fill(body,
      h("div", { class: "row jt-bar" },
        h("span", { class: "grow hint" }, "The exact bytes that would be sent. Redactions are marked."),
        h("button", { type: "button", class: "sm", onclick: () => all(true) }, "Expand all"),
        h("button", { type: "button", class: "sm", onclick: () => all(false) }, "Collapse all"),
        copy),
      tree);
  };
  for (const [id, label] of [["summary", "Summary"], ["json", "JSON"]]) {
    tabs.append(h("button", { type: "button", role: "tab", "data-tab": id, onclick: () => show(id) }, label));
  }
  show("summary");
  return h("div", { class: "sv-wrap" }, tabs, body);
}
