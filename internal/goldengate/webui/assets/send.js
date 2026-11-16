// Send: review first, then send. The review reads, summarizes and redacts
// every session exactly as a send would, and nothing leaves the computer.

import { api, events } from "/assets/api.js";
import { h, clear, plural, put, fill } from "/assets/dom.js";

const PLANNED = 0, PROJECT_START = 1, PROJECT_DONE = 2, PROJECT_FAILED = 3, FINISHED = 4, PREPARED = 5, PROGRESS = 6;

// run starts a dry or real send and feeds its events to on(); it resolves to
// the error text ("" when it went well).
async function run(dry, resummarize, on, onID) {
  const { id } = await api("/api/sync", { Dry: dry, Resummarize: resummarize });
  if (onID) onID(id);
  let error = "";
  await events(`/api/sync/${id}/events`, (name, data) => {
    if (name === "sync" && data) on(data);
    if (name === "end" && data) error = data.error || "";
  });
  return error;
}

export async function render(main, app) {
  const snap = await app.refresh();
  const box = h("div", null);
  fill(main, h("h1", null, "Send"), box);
  if (!snap.Server) {
    put(box, h("p", { class: "leaves" }, "No server is connected yet, so nothing can be sent."),
      h("button", { type: "button", class: "primary", onclick: () => app.go("setup") }, "Connect to your server"));
    return;
  }
  await review(box, app, false);
}

async function review(box, app, resummarize) {
  const prepared = h("p", { class: "dim" }, "Reading and redacting your sessions…");
  let runID = "";
  const cancel = h("button", { type: "button", onclick: () => { if (runID) api(`/api/sync/${runID}/cancel`, {}).catch(() => {}); } }, "Cancel");
  fill(box, 
    h("h2", null, "Step 1 of 2: Review"),
    h("p", { class: "leaves" }, "Nothing leaves this computer during the review."),
    prepared, h("div", { class: "row" }, cancel));
  let plan = null;
  let n = 0;
  let error = "";
  try {
    error = await run(true, resummarize, (e) => {
      if (e.Kind === PREPARED) { n++; prepared.textContent = `Reading and redacting your sessions… ${n} done (${e.Title || "untitled"})`; }
      if (e.Kind === PLANNED && e.Plan) plan = e.Plan;
    }, (id) => { runID = id; });
  } catch (e) {
    error = String(e.message || e);
  }
  if (error || !plan) {
    fill(box, h("p", { class: "bad" }, "The review did not finish: " + (error || "it was cancelled")),
      h("button", { type: "button", onclick: () => review(box, app, resummarize) }, "Review again"));
    return;
  }
  showPlan(box, app, plan, resummarize);
}

function showPlan(box, app, plan, resummarize) {
  const reds = Object.entries(plan.Redactions || {}).map(([k, v]) => `${k} ×${v}`).join(", ");
  const host = (plan.Server || "").replace(/^https?:\/\//, "");
  fill(box, 
    h("h2", null, "Step 1 of 2: Review"),
    plan.Sessions
      ? h("p", { class: "leaves" }, `Pressing Send uploads ${plural(plan.Sessions, "session")} from ${plural((plan.Projects || []).length, "project")} to ${host}. Nothing has been sent yet.`)
      : h("p", { class: "leaves" }, "Nothing to send: everything you share is already on the server, or nothing is shared."),
  );
  if ((plan.Projects || []).length) {
    put(box, h("table", null,
      h("thead", null, h("tr", null, h("th", null, "Project"), h("th", { class: "num" }, "Sessions to send"))),
      h("tbody", null, plan.Projects.map((p) => h("tr", null, h("td", null, p.Label), h("td", { class: "num" }, String(p.Sessions)))))));
  }
  const facts = [];
  if (plan.Approving) facts.push(`${plural(plan.Approving, "session")} waited for your approval: pressing Send approves ${plan.Approving === 1 ? "it" : "them"}.`);
  if (reds) facts.push(`Redacted before sending: ${reds}.`);
  if (plan.Unchanged) facts.push(`${plural(plan.Unchanged, "session")} already on the server and unchanged: not sent again.`);
  if (plan.NotSelected) facts.push(`${plural(plan.NotSelected, "session")} in projects you do not share: staying on this computer.`);
  if (plan.Excluded) facts.push(`${plural(plan.Excluded, "session")} excluded: never sent.`);
  if (plan.Warnings) facts.push(`${plural(plan.Warnings, "warning")} while reading sessions.`);
  if (plan.Summarizer) facts.push(`Summaries are written by ${plan.Summarizer}: the redacted conversations of the sessions being sent go to that model.`);
  for (const f of facts) put(box, h("p", { class: "dim" }, f));
  if (plan.Stale && !resummarize) {
    put(box, h("p", { class: "warn" }, `${plural(plan.Stale, "sent session")} still ${plan.Stale === 1 ? "has a" : "have"} rule-based ${plan.Stale === 1 ? "summary" : "summaries"}.`),
      h("button", { type: "button", onclick: () => review(box, app, true) }, `Include them (sends their redacted conversations to ${plan.Summarizer})`));
  }
  put(box, h("div", { class: "row" },
    h("button", { type: "button", onclick: () => review(box, app, resummarize) }, "Review again"),
    plan.Sessions ? h("button", { type: "button", class: "primary", onclick: () => sendNow(box, app, plan, resummarize) }, `Send ${plural(plan.Sessions, "session")}`) : null));
}

async function sendNow(box, app, plan, resummarize) {
  const rowsEl = h("tbody", null);
  const state = new Map();
  const draw = () => {
    clear(rowsEl);
    for (const [label, s] of state) {
      put(rowsEl, h("tr", null, h("td", null, label), h("td", { class: "num" }, `${s.sent} of ${s.total}`),
        h("td", { class: s.err ? "bad" : s.done ? "good" : "dim" }, s.err ? `✕ failed: ${s.err}` : s.done ? "✓ sent" : "sending…")));
    }
  };
  for (const p of plan.Projects || []) state.set(p.Label, { total: p.Sessions, sent: 0, done: false, err: "" });
  let runID = "";
  const cancel = h("button", { type: "button", onclick: () => { if (runID) api(`/api/sync/${runID}/cancel`, {}).catch(() => {}); } }, "Cancel");
  fill(box, h("h2", null, "Step 2 of 2: Send"),
    h("p", { class: "leaves" }, "Sending exactly what you reviewed."),
    h("table", null, h("thead", null, h("tr", null, h("th", null, "Project"), h("th", { class: "num" }, "Sessions"), h("th", null, "Status"))), rowsEl),
    h("div", { class: "row" }, cancel));
  draw();
  let error = "";
  try {
    error = await run(false, resummarize, (e) => {
      const s = state.get(e.Project) || (e.Project ? { total: e.Sessions || 0, sent: 0, done: false, err: "" } : null);
      if (!s) return;
      state.set(e.Project, s);
      if (e.Kind === PROJECT_START) s.total = e.Sessions || s.total;
      if (e.Kind === PROGRESS) s.sent = Math.min(s.total, s.sent + (e.Sessions || 0));
      if (e.Kind === PROJECT_DONE) { s.done = true; s.sent = s.total; }
      if (e.Kind === PROJECT_FAILED) s.err = e.Err || "failed";
      draw();
    }, (id) => { runID = id; });
  } catch (e) {
    error = String(e.message || e);
  }
  cancel.remove();
  const failed = [...state.values()].filter((s) => s.err).length;
  await app.refresh();
  if (error) put(box, h("p", { class: "bad" }, "The send stopped: " + error));
  if (failed || error) {
    put(box, h("p", { class: "warn" }, `${failed ? plural(failed, "project") + " failed. " : ""}What already arrived is not sent again.`),
      h("button", { type: "button", class: "primary", onclick: () => review(box, app, resummarize) }, "Review and retry"));
    app.say("Some of it was not sent.", true);
    return;
  }
  put(box, h("p", { class: "good" }, "✓ Sent. Your assistant can read it now."),
    h("button", { type: "button", onclick: () => app.go("projects") }, "Back to projects"));
  app.say("Sent.");
  void FINISHED;
}
