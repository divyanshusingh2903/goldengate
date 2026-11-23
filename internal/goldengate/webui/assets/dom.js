// Small DOM helpers. Everything that comes from a session (titles, paths,
// payloads) is untrusted text: it is only ever placed with textContent, never
// parsed as markup: nothing in this page assigns HTML strings.

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v === null || v === undefined) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k in el && k !== "list" && k !== "form") el[k] = v; // list and form are read-only properties: set as attributes
    else el.setAttribute(k, v === true ? "" : v);
  }
  add(el, children);
  return el;
}

function add(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) add(el, c);
    else el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

export function clear(el) {
  el.replaceChildren();
  return el;
}

// put appends children to el, skipping empty slots (null, undefined, false)
// and flattening arrays. The DOM's own append would print "null".
export function put(el, ...children) {
  add(el, children);
  return el;
}

// fill replaces what el holds.
export function fill(el, ...children) {
  return put(clear(el), ...children);
}

// when is "2h ago" for a Go time; "" for the zero time.
export function when(iso) {
  const t = Date.parse(iso);
  if (!iso || Number.isNaN(t) || iso.startsWith("0001-")) return "";
  const m = Math.max(0, Math.round((Date.now() - t) / 60000));
  if (m < 1) return "just now";
  if (m < 60) return `${m} min ago`;
  if (m < 48 * 60) return `${Math.round(m / 60)}h ago`;
  return `${Math.round(m / 1440)}d ago`;
}

// tag is a session's source as the terminal UI shows it ("CC", "GPT"): text
// that reads without colour; the colour says whether it is code or chat.
// SOURCES names each tool the way its maker does. Its logo is
// /assets/logos/<source>.svg, drawn as a mask in the text colour (app.css).
const SOURCES = {
  "claude-code": "Claude Code", codex: "Codex", opencode: "OpenCode", cursor: "Cursor",
  antigravity: "Antigravity", "claude-ai": "claude.ai", chatgpt: "ChatGPT",
};
// TOOLS are the sources, in the order a picker lists them.
export const TOOLS = Object.keys(SOURCES);
export function toolName(source) { return SOURCES[source] || String(source || "unknown"); }

// tag is a tool's logo, named for screen readers and on hover; with
// { name: true } the name is written beside it too. A tool without a logo
// shows its name in a small frame.
export function tag(source, opts) {
  const name = toolName(source);
  if (!SOURCES[source]) return h("span", { class: "tag", title: name }, name);
  const withName = opts && opts.name;
  return h("span", { class: `tool src-${source}`, title: name, ...(withName ? {} : { role: "img", "aria-label": name }) },
    h("span", { class: "lg", "aria-hidden": "true" }), withName ? h("span", { class: "tn" }, name) : null);
}

export function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many || one + "s"}`;
}

// ask shows a yes/no dialog and resolves to true on yes.
export function ask(title, lines, yesLabel) {
  const dlg = document.getElementById("confirm");
  document.getElementById("confirm-title").textContent = title;
  const body = clear(document.getElementById("confirm-body"));
  for (const l of lines) body.append(h("p", null, l));
  document.getElementById("confirm-yes").textContent = yesLabel || "Yes";
  return new Promise((resolve) => {
    dlg.addEventListener("close", () => resolve(dlg.returnValue === "yes"), { once: true });
    dlg.returnValue = "no";
    dlg.showModal();
    document.getElementById("confirm-no").focus();
  });
}

// marked splits text on [REDACTED:kind] markers so redactions stand out.
export function marked(text) {
  const out = [];
  let last = 0;
  for (const m of text.matchAll(/\[REDACTED:[^\]\n]{1,60}\]/g)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    out.push(h("mark", { title: "redacted before it leaves this computer" }, m[0]));
    last = m.index + m[0].length;
  }
  out.push(text.slice(last));
  return out;
}

// head is a screen's title, its one line, and its actions.
export function head(title, line, ...actions) {
  return h("div", { class: "page-head" },
    h("div", null, h("h1", null, title), line ? h("p", null, line) : null),
    actions.flat().some(Boolean) ? h("div", { class: "actions" }, actions) : null);
}

// card is a framed surface. head is a title (or a node) and its actions;
// body is placed as it is, so a caller picks card-body, a table or rows.
export function card(title, actions, ...body) {
  return h("section", { class: "card" },
    title ? h("div", { class: "card-head" }, typeof title === "string" ? h("h2", null, title) : title, actions ? h("div", { class: "actions" }, actions) : null) : null,
    body);
}

export function cardBody(...children) {
  return h("div", { class: "card-body" }, children);
}

// note says what leaves this computer, and when.
export function note(...text) {
  return h("p", { class: "note" }, text);
}

export function stat(key, value, sub, word) {
  return h("div", { class: "card stat" }, h("div", { class: "k" }, key), h("div", { class: word ? "v word" : "v" }, value), sub ? h("div", { class: "s" }, sub) : null);
}

export function empty(big, title, line, ...actions) {
  return h("div", { class: "empty" }, big ? h("div", { class: "big" }, big) : null, title ? h("h2", null, title) : null, line ? h("p", null, line) : null,
    actions.length ? h("div", { class: "actions" }, actions) : null);
}

// table wraps a table so a wide one scrolls inside its frame.
export function table(heads, rows) {
  return h("div", { class: "tbl" }, h("table", null,
    heads ? h("thead", null, h("tr", null, heads.map((x) => (x instanceof Node ? x : h("th", null, x))))) : null,
    h("tbody", null, rows)));
}

// drawer opens a side panel over the page; it closes with Esc, the scrim or
// its close button, and returns its body and foot to fill. onClose runs once.
export function drawer(title, top, onClose, opts) {
  const layer = document.getElementById("layer");
  const before = document.activeElement;
  const body = h("div", { class: "drawer-body" });
  const foot = h("div", { class: "drawer-foot" });
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    document.removeEventListener("keydown", onKey);
    clear(layer);
    if (before && before.isConnected) before.focus();
    if (onClose) onClose();
  };
  const onKey = (e) => { if (e.key === "Escape" && !document.getElementById("confirm").open) close(); };
  const x = h("button", { type: "button", class: "ghost icon", "aria-label": "Close", onclick: close }, "✕");
  const panel = h("aside", { class: "drawer" + (opts && opts.wide ? " wide" : ""), role: "dialog", "aria-modal": "true", "aria-label": title },
    h("div", { class: "drawer-head" }, h("div", { class: "row" }, h("span", { class: "actions" }, top), x), h("h2", null, title)),
    body, foot);
  fill(layer, h("div", { class: "scrim", onclick: close }), panel);
  document.addEventListener("keydown", onKey);
  x.focus();
  return { body, foot, close };
}

// Work item kinds in the order a review lists them, with their names. Each
// kind's icon is /assets/icons/<kind>.svg, drawn as a mask (app.css).
export const KINDS = [
  ["pull_request", "pull request", "pull requests"],
  ["issue", "issue", "issues"],
  ["commit", "commit", "commits"],
  ["artifact", "artifact", "artifacts"],
  ["file", "file", "files"],
];
const KIND = Object.fromEntries(KINDS.map(([k, one, many]) => [k, { one, many }]));

// workIcon is a work item kind's icon, named for screen readers.
export function workIcon(kind) {
  const k = KIND[kind] ? kind : "file";
  return h("span", { class: `wi wi-${k}`, role: "img", "aria-label": KIND[k].one });
}

// workCounts is one line: an icon and a count per kind, then the total.
export function workCounts(items) {
  const n = {};
  for (const w of items || []) n[w.kind] = (n[w.kind] || 0) + 1;
  const total = (items || []).length;
  if (!total) return h("span", { class: "wcounts faint" }, "no work items");
  return h("span", { class: "wcounts" },
    KINDS.filter(([k]) => n[k]).map(([k, one, many]) =>
      h("span", { class: "wc", title: plural(n[k], one, many) }, workIcon(k), h("span", null, String(n[k])))),
    h("span", { class: "faint" }, plural(total, "work item")));
}

// stamp is a time as "2h ago", with the full local time on hover.
export function stamp(iso) {
  const t = Date.parse(iso);
  if (!when(iso)) return null;
  return h("time", { datetime: iso, title: new Date(t).toLocaleString() }, when(iso));
}

// --- Toasts: every piece of feedback, warning and error ---
// A toast is text only (never markup). Good news and warnings leave on their
// own; an error stays until it is closed. A "busy" toast says something is
// under way and is replaced by whatever is said next.
const TOAST_LIFE = { ok: 4500, warn: 9000, busy: 0, bad: 0 };
const GLYPH = { ok: "✓", warn: "▲", bad: "✕", busy: "…" };
let lastSoft = null; // the newest ok/busy toast, replaced by the next one

export function toast(text, kind) {
  const k = TOAST_LIFE[kind] !== undefined ? kind : "ok";
  const box = document.getElementById("toasts");
  if (!box || !text) {
    if (!text && lastSoft) { lastSoft.remove(); lastSoft = null; }
    return null;
  }
  if (lastSoft && (k === "ok" || k === "busy")) { lastSoft.remove(); lastSoft = null; }
  let timer = 0;
  const close = () => { clearTimeout(timer); el.classList.add("out"); setTimeout(() => el.remove(), 160); if (lastSoft === el) lastSoft = null; };
  const el = h("div", { class: `toast ${k}`, role: k === "bad" ? "alert" : "status" },
    h("span", { class: "tg", "aria-hidden": "true" }, GLYPH[k]),
    h("span", { class: "tt" }, String(text)),
    h("button", { type: "button", class: "ghost icon tx", "aria-label": "Dismiss", onclick: close }, "✕"));
  const arm = () => { if (TOAST_LIFE[k]) timer = setTimeout(close, TOAST_LIFE[k]); };
  el.addEventListener("mouseenter", () => clearTimeout(timer));
  el.addEventListener("mouseleave", arm);
  box.append(el);
  // keep the stack short: drop the oldest that is not an error first
  while (box.children.length > 4) (([...box.children].find((c) => !c.classList.contains("bad"))) || box.firstChild).remove();
  if (k === "ok" || k === "busy") lastSoft = el;
  arm();
  return el;
}

// said is the message of an API reply, whichever way it is spelled.
export function said(reply, fallback) {
  return (reply && (reply.message || reply.Message)) || fallback || "";
}

// errText is an error's message.
export function errText(e) {
  return String((e && e.message) || e);
}

// --- Help: a link to the docs instead of paragraphs on the page ---
let docsBase = "";
export function setDocs(url) { docsBase = String(url || "").replace(/\/+$/, ""); }

// docs is a small "Docs" link to one page and section of the Golden Gate
// docs, e.g. docs("automation#rules"). It opens in a new tab.
export function docs(path, label) {
  if (!docsBase) return null;
  const [page, section] = String(path).split("#"); // the site serves each page at page/
  return h("a", { class: "docs", href: `${docsBase}/${page}/${section ? "#" + section : ""}`, target: "_blank", rel: "noopener noreferrer", title: "Open the docs in a new tab" },
    h("span", { "aria-hidden": "true" }, "?"), label || "Docs");
}

// more hides an explanation behind a small toggle; the text is there for
// whoever wants it and out of the way for everyone else.
export function more(...text) {
  return h("details", { class: "more" }, h("summary", null, "Why?"), h("p", { class: "hint" }, text));
}

// --- Loading: skeletons first, then the data ---
const bone = (cls) => h("span", { class: "bone " + (cls || "") });
export function skeleton(kind) {
  const headBones = h("div", { class: "page-head sk" }, h("div", null, bone("h1"), bone("line")), h("div", { class: "actions" }, bone("btn")));
  const rows = (n) => h("div", { class: "card" }, h("div", { class: "card-head" }, bone("h2")),
    Array.from({ length: n }, (_, i) => h("div", { class: "sk-row" }, bone("dot"), bone(i % 3 === 0 ? "w60" : i % 3 === 1 ? "w45" : "w70"), bone("w10"))));
  let body;
  if (kind === "stats") body = [h("div", { class: "grid g4" }, Array.from({ length: 4 }, () => h("div", { class: "card stat" }, bone("k"), bone("v")))), h("div", { class: "grid g2 gap-top" }, rows(5), rows(4))];
  else if (kind === "split") body = h("div", { class: "proj" }, rows(10), rows(4));
  else if (kind === "form") body = h("div", { class: "grid" }, rows(2), rows(4), rows(3));
  else body = [h("div", { class: "sk-tabs" }, bone("tab"), bone("tab"), bone("tab")), rows(8)];
  return h("div", { class: "skeleton", "aria-busy": "true", "aria-label": "Loading" }, headBones, body);
}

// batched appends rows (from make, one item at a time) to parent: the first
// batch at once, the rest a batch per frame, so a long list shows at once and
// the page stays responsive while the rest is drawn.
export function batched(parent, items, make, size) {
  const n = size || 60;
  let i = 0;
  const step = () => {
    const frag = document.createDocumentFragment();
    for (const end = Math.min(items.length, i + n); i < end; i++) put(frag, make(items[i], i));
    parent.append(frag);
    if (i < items.length) requestAnimationFrame(step);
  };
  step();
}
