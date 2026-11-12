package envconfig

import (
	"net"
	"net/url"
	"strings"
)

// ClientURL returns the URL that clients (TUI, CLI, API consumers) should
// connect to for inference requests. This is separate from the server bind
// address (Host / OLLAMA_HOST).
//
// Priority:
//  1. OLLAMA_URL — explicit client target (new, preferred)
//  2. OLLAMA_HOST — backward compatibility (old single-var behaviour)
//  3. Default: http://127.0.0.1:11434
func ClientURL() *url.URL {
	if v := strings.TrimSpace(Var("OLLAMA_URL")); v != "" {
		return parseHost(v)
	}
	// Fallback: use OLLAMA_HOST (covers old setups where one var did both jobs).
	return Host()
}

// BindAddress returns the address the server should listen on.
// Uses OLLAMA_BIND if set, otherwise falls back to OLLAMA_HOST for compat.
func BindAddress() *url.URL {
	if v := strings.TrimSpace(Var("OLLAMA_BIND")); v != "" {
		return parseHost(v)
	}
	return Host()
}

// parseHost parses a "host:port" or "scheme://host:port" string into a URL,
// applying the same defaults as Host() (scheme http, port 11434).
func parseHost(s string) *url.URL {
	s = strings.TrimSpace(s)
	scheme, hostport, ok := strings.Cut(s, "://")
	defaultPort := "11434"

	switch {
	case !ok:
		scheme, hostport = "http", s
	case scheme == "http":
		defaultPort = "80"
	case scheme == "https":
		defaultPort = "443"
	}

	hostport, path, _ := strings.Cut(hostport, "/")
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No colon found — treat whole thing as host with default port.
		if hostport == "" {
			host = "127.0.0.1"
		} else {
			host = hostport
		}
		port = defaultPort
	}

	return &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, port),
		Path:   path,
	}
}
