// The launch page. It turns the single-use launch token from the address
// bar into a session, keeps the request token in memory only, and shows
// that the local API answers. The screens themselves are separate work.

const status = document.getElementById("status");
const detail = document.getElementById("detail");

function say(text, more, bad) {
  status.textContent = text;
  status.className = bad ? "bad" : "";
  detail.hidden = !more;
  detail.textContent = more || "";
}

let requestToken = "";

// api calls the local API. The request token goes on every call; the session
// cookie is added by the browser. Session text is data: it is only ever put
// on the page with textContent.
export async function api(path, body) {
  const init = { headers: { "X-GoldenGate-Request": requestToken }, credentials: "same-origin" };
  if (body !== undefined) {
    init.method = "POST";
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (!res.ok) {
    let message = "the request was refused";
    try { message = (await res.json()).message || message; } catch { /* a bare refusal has no body */ }
    throw new Error(message);
  }
  return res.json();
}

async function start() {
  const token = location.hash.slice(1);
  // the token must not stay in the address bar or the history
  history.replaceState(null, "", "/launch");
  if (!token) {
    say("This page needs a fresh link.", "Run `goldengate ui` again, or press Enter in its terminal for a new link.", true);
    return;
  }
  try {
    const res = await fetch("/api/session", {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }),
    });
    if (!res.ok) throw new Error("refused");
    requestToken = (await res.json()).request_token;
  } catch {
    say("This link was already used or has expired.", "Press Enter in the terminal where `goldengate ui` runs for a new link.", true);
    return;
  }
  try {
    const snap = await api("/api/snapshot");
    const shared = (snap.Rows || []).filter((r) => r.State === 1 || r.State === 2).length;
    say("Connected to Golden Gate on this computer.",
      `${(snap.Rows || []).length} project folder(s) found, ${shared} shared. Nothing is sent by opening this page. ` +
      "The full interface is not built yet; the terminal UI (`goldengate`) has everything.");
  } catch (e) {
    say("Connected, but the first request failed.", String(e.message || e), true);
  }
}

start();
