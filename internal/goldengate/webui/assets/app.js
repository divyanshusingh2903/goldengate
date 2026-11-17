// Boot: turn the single-use launch code into a session, then show the
// screens. Each screen is a module with render(main, app).

import { api, setRequestToken } from "/assets/api.js";
import { h, fill } from "/assets/dom.js";
import * as setup from "/assets/setup.js";
import * as projects from "/assets/projects.js";
import * as preview from "/assets/preview.js";
import * as send from "/assets/send.js";
import * as server from "/assets/server.js";
import * as settings from "/assets/settings.js";
import * as inbox from "/assets/inbox.js";
import * as receipts from "/assets/receipts.js";
import * as agents from "/assets/agents.js";

const screens = { setup, projects, preview, send, server, settings, inbox, receipts, agents };
const main = document.getElementById("main");
const statusEl = document.getElementById("status");
const nav = document.getElementById("nav");

const app = {
  snapshot: null,
  // say shows one line of feedback; it stays until the next action.
  say(text, bad) {
    statusEl.textContent = text || "";
    statusEl.className = bad ? "status bad" : "status";
  },
  // refresh reloads what every screen needs and updates the header.
  async refresh() {
    app.snapshot = await api("/api/snapshot");
    const badge = document.getElementById("badge");
    badge.textContent = app.snapshot.Server ? `server: ${app.snapshot.Server.replace(/^https?:\/\//, "")}` : "server: not connected";
    return app.snapshot;
  },
  async go(name, params) {
    for (const b of nav.querySelectorAll("button")) {
      if (b.dataset.screen === name || (name === "preview" && b.dataset.screen === "projects") || (name === "receipts" && b.dataset.screen === "inbox")) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    }
    app.say(""); // feedback belongs to the screen it was given on
    // Each visit draws into its own element: an action that finishes after
    // you moved on redraws the screen you left, which is no longer shown.
    const view = h("div");
    fill(main, view);
    fill(view, h("p", { class: "dim" }, "Loading…"));
    try {
      await screens[name].render(view, app, params || {});
    } catch (e) {
      fill(view, h("p", { class: "bad" }, String(e.message || e)));
    }
    main.focus();
  },
};

function fail(text, more) {
  fill(main, h("h1", null, text), h("p", { class: "dim" }, more));
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
  // first run, or no server yet: start with setup, as the terminal UI does
  app.go(app.snapshot.FirstRun || !app.snapshot.Server ? "setup" : "projects");
}

// A fresh link pasted into this tab only changes the part after "#", which
// the browser treats as the same page: start over so the new code is used.
window.addEventListener("hashchange", () => location.reload());

start();
