package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// CheckServer applies the HTTPS-only rule to a server URL and returns it
// normalised: lower-case scheme and host, no default port, no trailing
// slash. Requests go over HTTPS only. An http:// URL is refused unless its
// host is loopback (localhost, 127.0.0.0/8, ::1), for a server on the
// same machine (the design's "Storage and revocation").
//
// The URL is the server's root: the client adds /api/v1 itself. A user
// name or password, a query and a fragment are refused, and errors never
// repeat a password.
func CheckServer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("no server URL: give an https:// URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("server URL %s: not a URL", redact(raw))
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" || (u.Host == "" && u.Opaque != "") {
		// "loomux.example.net", "loomux.example.net:443", "//host",
		// "localhost:8080": a host without a scheme. url.Parse reads the
		// ones with a port as scheme "host" and opaque "port".
		return "", fmt.Errorf("server URL %s has no scheme: use https://%s", redact(raw), strings.TrimPrefix(redact(raw), "//"))
	}
	if u.User != nil {
		return "", fmt.Errorf("server URL %s has a user name or password: remove it (the token is stored apart from the config)", redact(raw))
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("server URL %s has no host", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("server URL %s has a query: give the server's root URL", raw)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("server URL %s has a fragment: give the server's root URL", raw)
	}
	host := strings.ToLower(u.Hostname())
	switch scheme {
	case "https":
	case "http":
		if !loopback(host) {
			return "", fmt.Errorf("server URL %s: requests go over HTTPS only, use https:// (http:// is allowed only for localhost)", raw)
		}
	default:
		return "", fmt.Errorf("server URL %s: the scheme must be https:// (http:// only for localhost)", raw)
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	for _, api := range []string{"/api/v1", "/api"} {
		if strings.HasSuffix(path, api) {
			return "", fmt.Errorf("server URL %s ends in %s: give the server's root URL (the client adds /api/v1)", raw, api)
		}
	}

	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	hostport := host
	if strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport = net.JoinHostPort(host, port)
	}
	return scheme + "://" + hostport + path, nil
}

// loopback reports whether host, without a port, is this machine:
// "localhost" or a loopback IP address. A name that only starts like one
// (localhost.example.net, 127.0.0.1.example.net) isn't.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redact returns raw with any user name and password removed, for error
// messages.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		// Without a scheme, "user:pass@host" doesn't parse as userinfo:
		// drop everything up to the last "@" before the first "/".
		rest := raw
		if i := strings.Index(rest, "://"); i >= 0 {
			rest = rest[i+3:]
		}
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
			return strings.Replace(raw, rest[:at+1], "", 1)
		}
		return raw
	}
	u.User = nil
	return u.String()
}
