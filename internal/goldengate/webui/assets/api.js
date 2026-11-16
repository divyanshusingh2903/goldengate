// The page's only door to the local API. The request token lives in this
// module's memory and nowhere else; the session cookie is the browser's.

let requestToken = "";

export function setRequestToken(t) {
  requestToken = t;
}

function headers(body) {
  const hd = { "X-GoldenGate-Request": requestToken };
  if (body !== undefined) hd["Content-Type"] = "application/json";
  return hd;
}

async function failure(res) {
  let message = res.status === 403 ? "This page's session has ended. Run `goldengate ui` again for a new link." : "the request was refused";
  try {
    const j = await res.json();
    if (j.message) message = j.message;
    const err = new Error(message);
    err.kind = j.error;
    return err;
  } catch {
    return new Error(message); // a refused request has no body
  }
}

// api calls one endpoint: GET without a body, POST with one.
export async function api(path, body) {
  const init = { headers: headers(body), credentials: "same-origin" };
  if (body !== undefined) {
    init.method = "POST";
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (!res.ok) throw await failure(res);
  return res.json();
}

// events reads a server-sent event stream and calls on(event, data) for each
// event until the stream ends. EventSource cannot send our request token, so
// this reads the stream with fetch.
export async function events(path, on) {
  const res = await fetch(path, { headers: headers(), credentials: "same-origin" });
  if (!res.ok) throw await failure(res);
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buf += dec.decode(value, { stream: true });
    let cut;
    while ((cut = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, cut);
      buf = buf.slice(cut + 2);
      let name = "message";
      let data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event: ")) name = line.slice(7);
        else if (line.startsWith("data: ")) data += line.slice(6);
      }
      let parsed = null;
      try { parsed = JSON.parse(data); } catch { /* not for us */ }
      on(name, parsed);
    }
  }
}
