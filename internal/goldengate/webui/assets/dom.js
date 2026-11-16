// Small DOM helpers. Everything that comes from a session (titles, paths,
// payloads) is untrusted text: it is only ever placed with textContent, never
// parsed as markup: nothing in this page assigns HTML strings.

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v === null || v === undefined) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k in el && k !== "list") el[k] = v;
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
