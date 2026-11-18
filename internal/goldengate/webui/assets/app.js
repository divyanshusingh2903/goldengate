// Boot: turn the single-use launch code into a session, then show the
// screens. Each screen is a module with render(main, app).

import { api, setRequestToken } from "/assets/api.js";
import { h, fill, head } from "/assets/dom.js";
import * as setup from "/assets/setup.js";
import * as projects from "/assets/projects.js";
import * as preview from "/assets/preview.js";
import * as send from "/assets/send.js";
import * as server from "/assets/server.js";
import * as settings from "/assets/settings.js";
import * as inbox from "/assets/inbox.js";
import * as receipts from "/assets/receipts.js";
import * as agents from "/assets/agents.js";
import * as overview from "/assets/overview.js";

const screens = { overview, setup, projects, preview, send, server, settings, inbox, receipts, agents };
const main = document.getElementById("main");
const statusEl = document.getElementById("status");
const nav = document.getElementById("nav");
const sendBtn = document.getElementById("send-btn");
// Screens reached from another one keep that one lit in the sidebar.
const parent = { preview: "projects" };
// Screens with a list beside a detail use the whole window.
const wide = new Set(["projects"]);

// count shows a number beside a screen in the sidebar, or nothing for zero.
function count(id, n) {
  const el = document.getElementById(id);
  el.textContent = n ? String(n) : "";
  el.title = n ? `${n} waiting` : "";
}

const app = {
  snapshot: null,
  // say shows one line of feedback; it stays until the next action.
  say(text, bad) {
    statusEl.textContent = text || "";
    statusEl.className = bad ? "status bad" : "status";
  },
  // refresh reloads what every screen needs and updates the sidebar.
  async refresh() {
    const [snap, inbox] = await Promise.all([api("/api/snapshot"), api("/api/inbox").catch(() => null)]);
    app.snapshot = snap;
    const host = snap.Server ? snap.Server.replace(/^https?:\/\//, "") : "";
    document.getElementById("badge").textContent = host || "not connected";
    document.getElementById("badge").title = host ? `server: ${host}` : "no server connected";
    document.getElementById("server-dot").className = host ? "dot" : "dot off";
    const toSend = (snap.Rows || []).filter((r) => (r.State === 1 || r.State === 2) && r.Pending > 0).reduce((n, r) => n + r.Pending, 0);
    count("count-inbox", inbox ? (inbox.Pending || []).length : 0);
    count("count-send", toSend);
    sendBtn.hidden = !host;
    sendBtn.textContent = toSend ? `Review and send ${toSend}` : "Review and send";
    return snap;
  },
  async go(name, params) {
    for (const b of nav.querySelectorAll("button")) {
      if (b.dataset.screen === (parent[name] || name)) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    }
    app.say(""); // feedback belongs to the screen it was given on
    // Each visit draws into its own element: an action that finishes after
    // you moved on redraws the screen you left, which is no longer shown.
    const view = h("div");
    main.classList.toggle("wide", wide.has(name));
    fill(main, view);
    fill(view, h("p", { class: "dim" }, "Loading…"));
    try {
      await screens[name].render(view, app, params || {});
    } catch (e) {
      fill(view, h("p", { class: "bad" }, String(e.message || e)));
    }
    document.querySelector(".scroll").scrollTop = 0;
    main.focus();
  },
};

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
    setRequestToken((await res.json()).request_token);
  } catch {
    fail("This link was already used or has expired.", "Press Enter in the terminal where `goldengate ui` runs for a new link.");
    return;
  }
  try {
    await app.refresh();
  } catch (e) {
    fail("Connected, but the first request failed.", String(e.message || e));
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

start();
