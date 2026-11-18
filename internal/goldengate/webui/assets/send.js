// Send: review first, then send. The review reads, summarizes and redacts
// every session exactly as a send would, and nothing leaves the computer.

import { api, events } from "/assets/api.js";
import { h, clear, plural, put, fill, head, card, cardBody, note, empty, table } from "/assets/dom.js";

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

// stepper shows where the send stands: 1 review, 2 send, 3 done.
function stepper(step) {
  const part = (n, label) => h("span", { class: step > n ? "done" : step === n ? "on" : "" }, h("b", null, step > n ? "✓" : String(n)), label);
  return h("div", { class: "stepper", "aria-label": `Step ${Math.min(step, 2)} of 2` }, part(1, "Review"), h("i"), part(2, "Send"));
}

export async function render(main, app) {
  const snap = await app.refresh();
  const box = h("div", null);
  const host = (snap.Server || "").replace(/^https?:\/\//, "");
  fill(main, head("Send", host ? `Review what leaves, then send it to ${host}.` : "Review what leaves, then send it to your server."), box);
  if (!snap.Server) {
    put(box, h("div", { class: "card" }, empty("↗", "No server yet", "No server is connected yet, so nothing can be sent.",
      h("button", { type: "button", class: "primary", onclick: () => app.go("setup") }, "Connect to your server"))));
    return;
  }
  await review(box, app, false);
}

async function review(box, app, resummarize) {
  const prepared = h("p", { class: "dim" }, "Reading and redacting your sessions…");
  let runID = "";
  const cancel = h("button", { type: "button", onclick: () => { if (runID) api(`/api/sync/${runID}/cancel`, {}).catch(() => {}); } }, "Cancel");
  fill(box,
    stepper(1),
    card(null, null, cardBody(prepared, h("progress", { "aria-label": "Reading sessions" })), h("div", { class: "card-foot" }, cancel)),
    note("Nothing leaves this computer during the review."));
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
    fill(box, stepper(1), h("div", { class: "card" }, empty("✕", "The review did not finish", error || "It was cancelled.",
      h("button", { type: "button", onclick: () => review(box, app, resummarize) }, "Review again"))));
    return;
  }
  showPlan(box, app, plan, resummarize);
}

function showPlan(box, app, plan, resummarize) {
  const reds = Object.entries(plan.Redactions || {}).map(([k, v]) => `${k} ×${v}`).join(", ");
  const host = (plan.Server || "").replace(/^https?:\/\//, "");
  const facts = [];
  if (plan.Approving) facts.push(`${plural(plan.Approving, "session")} waited for your approval: pressing Send approves ${plan.Approving === 1 ? "it" : "them"}.`);
  if (reds) facts.push(`Redacted before sending: ${reds}.`);
  if (plan.Unchanged) facts.push(`${plural(plan.Unchanged, "session")} already on the server and unchanged: not sent again.`);
  if (plan.NotSelected) facts.push(`${plural(plan.NotSelected, "session")} in projects you do not share: staying on this computer.`);
  if (plan.Excluded) facts.push(`${plural(plan.Excluded, "session")} excluded: never sent.`);
  if (plan.Warnings) facts.push(`${plural(plan.Warnings, "warning")} while reading sessions.`);
  if (plan.Summarizer) facts.push(`Summaries are written by ${plan.Summarizer}: the redacted conversations of the sessions being sent go to that model.`);

  const projects = plan.Projects || [];
  fill(box,
    stepper(1),
    card(plan.Sessions ? `${plural(plan.Sessions, "session")} ready` : "Nothing to send", null,
      projects.length
        ? table(["Project", h("th", { class: "num" }, "Sessions to send")], projects.map((p) => h("tr", null, h("td", null, p.Label), h("td", { class: "num" }, String(p.Sessions)))))
        : empty("✓", null, "Everything you share is already on the server, or nothing is shared."),
      facts.length ? h("div", { class: "card-body" }, h("ul", { class: "dim small" }, facts.map((f) => h("li", null, f)))) : null,
      plan.Stale && !resummarize ? h("div", { class: "card-body" },
        h("p", { class: "warn" }, `${plural(plan.Stale, "sent session")} still ${plan.Stale === 1 ? "has a" : "have"} rule-based ${plan.Stale === 1 ? "summary" : "summaries"}.`),
        h("div", { class: "row" }, h("button", { type: "button", onclick: () => review(box, app, true) }, `Include them (sends their redacted conversations to ${plan.Summarizer})`))) : null,
      h("div", { class: "card-foot" },
        h("span", { class: "grow dim" }, plan.Sessions ? `Pressing Send uploads them to ${host}. Nothing has been sent yet.` : ""),
        h("button", { type: "button", onclick: () => review(box, app, resummarize) }, "Review again"),
        plan.Sessions ? h("button", { type: "button", class: "primary", onclick: () => sendNow(box, app, plan, resummarize) }, `Send ${plural(plan.Sessions, "session")}`) : null)),
    note("Only the summaries reviewed here leave. Raw transcripts stay on this computer."));
}

async function sendNow(box, app, plan, resummarize) {
  const rowsEl = h("tbody", null);
  const state = new Map();
  const total = (plan.Projects || []).reduce((n, p) => n + p.Sessions, 0) || 1;
  const bar = h("progress", { max: String(total), value: "0", "aria-label": "Sessions sent" });
  const draw = () => {
    clear(rowsEl);
    let sent = 0;
    for (const [label, s] of state) {
      sent += s.sent;
      put(rowsEl, h("tr", null, h("td", null, label), h("td", { class: "num" }, `${s.sent} of ${s.total}`),
        h("td", { class: s.err ? "bad" : s.done ? "good" : "dim" }, s.err ? `✕ failed: ${s.err}` : s.done ? "✓ sent" : "sending…")));
    }
    bar.value = sent;
  };
  for (const p of plan.Projects || []) state.set(p.Label, { total: p.Sessions, sent: 0, done: false, err: "" });
  let runID = "";
  const cancel = h("button", { type: "button", onclick: () => { if (runID) api(`/api/sync/${runID}/cancel`, {}).catch(() => {}); } }, "Cancel");
  const foot = h("div", { class: "card-foot" }, h("div", { class: "grow" }, bar), cancel);
  fill(box, stepper(2),
    card("Sending exactly what you reviewed", null,
      h("div", { class: "tbl" }, h("table", null, h("thead", null, h("tr", null, h("th", null, "Project"), h("th", { class: "num" }, "Sessions"), h("th", null, "Status"))), rowsEl)),
      foot));
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
  if (failed || error) {
    put(box, h("div", { class: "card gap-top" }, empty("✕", error ? "The send stopped" : `${plural(failed, "project")} failed`,
      `${error ? error + ". " : ""}What already arrived is not sent again.`,
      h("button", { type: "button", class: "primary", onclick: () => review(box, app, resummarize) }, "Review and retry"))));
    app.say("Some of it was not sent.", true);
    return;
  }
  const sentN = [...state.values()].reduce((n, s) => n + s.sent, 0);
  const done = empty("✓", `Sent ${plural(sentN, "session")}`, "Your assistant can read it now.",
    h("button", { type: "button", onclick: () => app.go("inbox") }, "Inbox"),
    h("button", { type: "button", class: "primary", onclick: () => app.go("projects") }, "Back to projects"));
  done.firstChild.classList.add("good");
  fill(box, stepper(3), h("div", { class: "card" }, done));
  app.say("Sent.");
  void FINISHED;
}
