package server

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// requireKnownProtocolVersion answers an MCP-Protocol-Version the server does
// not implement with 400 and a JSON-RPC UnsupportedProtocolVersionError
// listing the versions it does, as the Streamable HTTP transport requires. The
// SDK does this itself only for versions from 2026-07-28 on that carry the
// per-request _meta; older or malformed versions get a plain-text 400 there,
// which modern clients cannot tell apart from a legacy server.
func requireKnownProtocolVersion(next http.Handler) http.Handler {
	supported := mcp.SupportedProtocolVersions()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Values("Mcp-Protocol-Version")
		if len(v) == 0 || (len(v) == 1 && slices.Contains(supported, v[0])) {
			next.ServeHTTP(w, r)
			return
		}
		requested := v[0]
		if len(v) > 1 {
			requested = ""
		}
		data, _ := json.Marshal(mcp.UnsupportedProtocolVersionData{Supported: supported, Requested: requested})
		resp := struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Error   any             `json:"error"`
		}{"2.0", requestID(r), map[string]any{
			"code": mcp.CodeUnsupportedProtocolVersion, "message": "unsupported protocol version", "data": json.RawMessage(data)}}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(append(b, '\n'))
	})
}

// requestID returns the id of a single JSON-RPC request in the body, or nil
// when there is none to echo (a notification, a batch, or a malformed body).
func requestID(r *http.Request) json.RawMessage {
	if r.Body == nil {
		return nil
	}
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&msg) != nil {
		return nil
	}
	switch id := string(msg.ID); {
	case id == "" || id == "null":
		return nil
	case id[0] == '"' || (id[0] >= '0' && id[0] <= '9') || id[0] == '-':
		return msg.ID
	}
	return nil
}
