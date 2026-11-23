// Boot: turn the single-use launch code into a session, then show the
// screens. Each screen is a module with render(main, app).

import { api, setRequestToken } from "/assets/api.js";
import { h, fill, head, empty, toast, errText, setDocs, skeleton, ask } from "/assets/dom.js";
import * as setup from "/assets/setup.js";
import * as projects from "/assets/projects.js";
import * as preview from "/assets/preview.js";
import * as send from "/assets/send.js";
import * as server from "/assets/server.js";
import * as settings from "/assets/settings.js";
import * as inbox from "/assets/inbox.js";
import * as receipts from "/assets/receipts.js";
import * as rules from "/assets/rules.js";
import * as agents from "/assets/agents.js";
import * as overview from "/assets/overview.js";

const screens = { overview, setup, projects, preview, send, server, settings, inbox, receipts, rules, agents };
const main = document.getElementById("main");
const nav = document.getElementById("nav");
const sendBtn = document.getElementById("send-btn");
const busyBar = document.getElementById("busy");
// Screens reached from another one keep that one lit in the sidebar.
const parent = { preview: "projects" };
// Screens with a list beside a detail use the whole window.
const wide = new Set(["projects"]);
// The skeleton each screen shows while its first data loads.
const shape = { overview: "stats", projects: "split", settings: "form", setup: "form", server: "stats", send: "form" };
// Screens that may show what they showed last time while fresh data loads.
// The others start a process (a review), hold a form, or depend on params.
const reuse = new Set(["overview", "inbox", "projects", "receipts", "rules", "server", "agents"]);
const views = new Map();

// count shows a number beside a screen in the sidebar, or nothing for zero.
function count(id, n) {
  const el = document.getElementById(id);
  el.textContent = n ? String(n) : "";
  el.title = n ? `${n} waiting` : "";
}

let inboxCall = null; // the inbox request in flight, shared by whoever asks
let goSeq = 0;
const told = new Set(); // warnings already shown once this session

const app = {
  snapshot: null,
  // say shows feedback as a toast: kind true or "bad" for an error (it
  // stays until closed), "warn" for a warning, "busy" for something under
  // way; anything else is good news. say("") takes back the last good news.
  say(text, kind) {
    toast(text, kind === true ? "bad" : kind || "ok");
  },
  // once says a lasting warning a single time per session, not on every visit.
  once(text, kind) {
    if (!text || told.has(text)) return;
    told.add(text);
    app.say(text, kind || "warn");
  },
  // leave is set by a screen with unsaved changes: it resolves to true when
  // the screen may be left. go() asks it first, and drops it after.
  leave: null,
  // inbox loads the inbox once for every screen that asks at the same time:
  // it is the slowest call, so it is never made twice in parallel.
  inbox() {
    if (!inboxCall) {
      inboxCall = api("/api/inbox").then((v) => { count("count-inbox", (v.Pending || []).length); return v; })
        .finally(() => { inboxCall = null; });
    }
    return inboxCall;
  },
  // refresh reloads the snapshot and updates the sidebar and send button. The
  // inbox count follows on its own, so a screen never waits for it.
  async refresh() {
    app.inbox().catch(() => {});
    const snap = await api("/api/snapshot");
    app.snapshot = snap;
    const host = snap.Server ? snap.Server.replace(/^https?:\/\//, "") : "";
    document.getElementById("badge").textContent = host || "not connected";
    document.getElementById("badge").title = host ? `server: ${host}` : "no server connected";
    document.getElementById("server-dot").className = host ? "dot" : "dot off";
    const toSend = (snap.Rows || []).filter((r) => (r.State === 1 || r.State === 2) && r.Pending > 0).reduce((n, r) => n + r.Pending, 0);
    count("count-send", toSend);
    sendBtn.hidden = !host;
    sendBtn.textContent = toSend ? `Review and send ${toSend}` : "Review and send";
    return snap;
  },
  async go(name, params) {
    if (app.leave && !(await app.leave())) return false;
    app.leave = null;
    app.dirty = null;
    for (const b of nav.querySelectorAll("button")) {
      if (b.dataset.screen === (parent[name] || name)) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    }
    app.say(""); // good news belongs to the screen it was given on
    // Each visit draws into the screen's own element. A screen visited
    // before shows what it showed then until fresh data replaces it; a first
    // visit shows a skeleton. An action that finishes after you moved on
    // redraws an element that is no longer shown.
    const key = name + JSON.stringify(params || {});
    let view = reuse.has(name) ? views.get(key) : null;
    const first = !view;
    if (first) {
      view = h("div");
      fill(view, skeleton(shape[name]));
      if (reuse.has(name)) views.set(key, view);
    }
    main.classList.toggle("wide", wide.has(name));
    fill(main, view);
    document.querySelector(".scroll").scrollTop = 0;
    main.focus();
    const me = ++goSeq;
    busyBar.hidden = false;
    try {
      await screens[name].render(view, app, params || {});
    } catch (e) {
      app.say(errText(e), true);
      if (first || view.querySelector(".skeleton")) {
        fill(view, head(title(name)), h("div", { class: "card" }, empty("✕", "This screen could not load", errText(e),
          h("button", { type: "button", onclick: () => app.go(name, params) }, "Try again"))));
      }
    } finally {
      if (me === goSeq) busyBar.hidden = true;
    }
    return true;
  },
};

function title(name) {
  const b = nav.querySelector(`button[data-screen="${parent[name] || name}"]`);
  return b ? b.textContent.replace(/\d+$/, "").trim() : name;
}

function fail(text, more) {
  fill(main, head(text, more));
}

function theme() {
  const root = document.documentElement;
  let saved = null;
  try { saved = localStorage.getItem("gg-theme"); } catch { /* private mode */ }
  if (saved === "light" || saved === "dark") root.dataset.theme = saved;
  document.getElementById("theme").addEventListener("click", () => {
    const dark = root.dataset.theme ? root.dataset.theme === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    root.dataset.theme = dark ? "light" : "dark";
    try { localStorage.setItem("gg-theme", root.dataset.theme); } catch { /* private mode */ }
  });
}

async function start() {
  theme();
  const token = location.hash.slice(1);
  history.replaceState(null, "", "/launch"); // the code must not stay in the address bar or the history
  if (!token) {
    fail("This page needs a fresh link.", "Run `goldengate ui` again, or press Enter in its terminal for a new link. (Reloading the page ends its session on purpose.)");
    return;
  }
  try {
    const res = await fetch("/api/session", {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }),
    });
    if (!res.ok) throw new Error("refused");
    const s = await res.json();
    setRequestToken(s.request_token);
    setDocs(s.docs);
  } catch {
    fail("This link was already used or has expired.", "Press Enter in the terminal where `goldengate ui` runs for a new link.");
    return;
  }
  try {
    await app.refresh();
  } catch (e) {
    fail("Connected, but the first request failed.", errText(e));
    return;
  }
  nav.hidden = false;
  for (const b of nav.querySelectorAll("button")) b.addEventListener("click", () => app.go(b.dataset.screen));
  sendBtn.addEventListener("click", () => app.go("send"));
  // first run, or no server yet: start with setup, as the terminal UI does
  app.go(app.snapshot.FirstRun || !app.snapshot.Server ? "setup" : "overview");
}

// A fresh link pasted into this tab only changes the part after "#", which
// the browser treats as the same page: start over so the new code is used.
window.addEventListener("hashchange", () => location.reload());

// Closing or reloading the tab with unsaved changes asks the browser's own
// question; the page cannot word it.
window.addEventListener("beforeunload", (e) => {
  if (app.dirty && app.dirty()) { e.preventDefault(); e.returnValue = ""; }
});

// unsaved is the question a screen with a form asks before it is left.
app.unsaved = () => ask("Leave without saving?", ["Your changes on this screen are not saved yet."], "Leave without saving");

start();
