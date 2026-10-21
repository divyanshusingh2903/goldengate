package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// originSet is the browser origins allowed to call /mcp. The bridge serves no
// page of its own, so no origin is trusted by default: a request whose Origin
// matches its own Host proves nothing under DNS rebinding, because the
// attacker's page and the rebound Host carry the same name.
type originSet map[string]bool

func newOriginSet(publicURL string, extra []string) (originSet, error) {
	set := originSet{}
	if publicURL != "" {
		o, err := normalizeOrigin(publicURL)
		if err != nil {
			return nil, fmt.Errorf("public URL: %w", err)
		}
		set[o] = true
	}
	for _, v := range extra {
		o, err := normalizeOrigin(v)
		if err != nil {
			return nil, fmt.Errorf("allowed origin %q: %w", v, err)
		}
		set[o] = true
	}
	return set, nil
}

// allows reports whether a request may proceed: one without an Origin header
// comes from a non-browser client and always may.
func (s originSet) allows(r *http.Request) bool {
	v := r.Header.Values("Origin")
	if len(v) == 0 {
		return true
	}
	if len(v) > 1 {
		return false
	}
	o, err := normalizeOrigin(v[0])
	return err == nil && s[o]
}

// normalizeOrigin reduces scheme://host[:port] to the form browsers send in
// the Origin header: lower case, default port dropped, no path.
func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("want an http or https origin")
	}
	if u.Host == "" || u.User != nil || strings.TrimSuffix(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("want scheme://host[:port] with nothing after it")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

// forbiddenOrigin answers 403 with a JSON-RPC error that has no id, as the
// Streamable HTTP transport allows.
func forbiddenOrigin(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"forbidden: Origin is not allowed"}}` + "\n"))
}
