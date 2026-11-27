package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTest starts h on a loopback server (http:// is allowed there) and
// returns a client of it with token tok.
func newTest(t *testing.T, tok string, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, tok)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewAppliesTheHTTPSRule(t *testing.T) {
	for _, bad := range []string{"http://loomux.example.net", "loomux.example.net", "https://loomux.example.net/api/v1", "https://user:pw@loomux.example.net"} {
		if _, err := New(bad, "tok"); err == nil {
			t.Errorf("New(%q) succeeded, want the HTTPS-only rule's error", bad)
		}
	}
	c, err := New("https://loomux.example.net/", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Server(); got != "https://loomux.example.net" {
		t.Errorf("Server() = %q, want the normalised root", got)
	}
}

func TestRequestsCarryTheTokenAndAcceptJSON(t *testing.T) {
	var got *http.Request
	c := newTest(t, "secret-token", func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api_version":"v1","server_version":"0.3.155"}`)
	})
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.APIVersion != "v1" || v.ServerVersion != "0.3.155" {
		t.Errorf("Version = %+v", v)
	}
	if got.URL.Path != "/api/v1/version" || got.Method != http.MethodGet {
		t.Errorf("request %s %s", got.Method, got.URL.Path)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer secret-token" {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.Header.Get("Accept"); h != "application/json" {
		t.Errorf("Accept = %q", h)
	}
	if h := got.Header.Get("User-Agent"); !strings.HasPrefix(h, "loomux/") {
		t.Errorf("User-Agent = %q, want loomux/<version>", h)
	}
}

func TestNoTokenSendsNoAuthorization(t *testing.T) {
	c := newTest(t, "", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("Authorization sent without a token: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api_version":"v1","server_version":"x"}`)
	})
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPathSegmentsAreEscaped(t *testing.T) {
	var raw string
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	if _, err := c.Dispatch(context.Background(), "a/b?c"); err != nil {
		t.Fatal(err)
	}
	if raw != "/api/v1/dispatches/a%2Fb%3Fc" {
		t.Errorf("path = %q, want the id as one escaped segment", raw)
	}
}

func TestErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantText   string
		dispatchID string
		holdID     string
		ended      string
	}{
		{"a specific code", 409, `{"error":"conversation busy","code":"conversation_busy","dispatch_id":"d-1"}`, CodeConversationBusy, "conversation busy", "d-1", "", ""},
		{"the hold of task_held", 409, `{"error":"held by laptop","code":"task_held","hold":{"id":"h-1","mode":"drive","client_label":"laptop"}}`, CodeTaskHeld, "held by laptop", "", "h-1", ""},
		{"hold_ended's reason", 410, `{"error":"the hold has ended","code":"hold_ended","ended_reason":"expired","hold":{"id":"h-2"}}`, CodeHoldEnded, "the hold has ended", "", "h-2", "expired"},
		// A server older than the code, or a code added later: the
		// envelope's code is kept, and Is matches the status default.
		{"an unknown code", 409, `{"error":"something new","code":"brand_new"}`, "brand_new", "something new", "", "", ""},
		{"no code", 404, `{"error":"no such task"}`, CodeNotFound, "no such task", "", "", ""},
		{"not the envelope", 500, `{"nope":1}`, CodeInternal, "500 Internal Server Error", "", "", ""},
		{"an empty JSON body", 503, ``, CodeUnavailable, "503 Service Unavailable", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			_, err := c.Dispatch(context.Background(), "d")
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v (%T), want *Error", err, err)
			}
			if e.Status != tc.status || e.Code != tc.wantCode || e.Error() != tc.wantText {
				t.Errorf("got status %d code %q text %q", e.Status, e.Code, e.Error())
			}
			if e.DispatchID != tc.dispatchID || e.EndedReason != tc.ended {
				t.Errorf("dispatch_id %q ended_reason %q", e.DispatchID, e.EndedReason)
			}
			if (e.Hold == nil) != (tc.holdID == "") || (e.Hold != nil && e.Hold.ID != tc.holdID) {
				t.Errorf("hold = %+v, want id %q", e.Hold, tc.holdID)
			}
			if !Is(err, DefaultCode(tc.status)) {
				t.Errorf("Is(err, %q) = false: every error matches its status's default", DefaultCode(tc.status))
			}
		})
	}
}

func TestIs(t *testing.T) {
	busy := &Error{Status: 409, Code: CodeConversationBusy}
	if !Is(busy, CodeConversationBusy) || !Is(busy, CodeConflict) {
		t.Error("conversation_busy is both itself and a conflict")
	}
	if Is(busy, CodeTaskHeld) || Is(busy, CodeNotFound) {
		t.Error("conversation_busy isn't task_held or not_found")
	}
	if Is(errors.New("x"), CodeConflict) || Is(nil, CodeConflict) {
		t.Error("a non-API error matches no code")
	}
	if !Unauthorized(&Error{Status: 401, Code: CodeUnauthorized}) || Unauthorized(busy) {
		t.Error("Unauthorized is the 401")
	}
}

// A joined error's 401 isn't hidden by an API error before it: a release's
// 403 or 503 must not mask the heartbeat's 401 beside it.
func TestUnauthorizedLooksThroughAJoinedError(t *testing.T) {
	unauthorized := &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized}
	forbidden := &Error{Status: http.StatusForbidden, Code: CodeForbidden}
	unavailable := &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"forbidden then unauthorized":   {errors.Join(forbidden, unauthorized), true},
		"unavailable then unauthorized": {errors.Join(unavailable, unauthorized), true},
		"wrapped join":                  {fmt.Errorf("release: %w", errors.Join(forbidden, unauthorized)), true},
		"non-401s":                      {errors.Join(forbidden, unavailable), false},
		"one non-401":                   {forbidden, false},
		"nil":                           {nil, false},
	} {
		if got := Unauthorized(tc.err); got != tc.want {
			t.Errorf("%s: Unauthorized = %v, want %v", name, got, tc.want)
		}
	}
}

func TestDefaultCode(t *testing.T) {
	for status, want := range map[int]string{400: "invalid_request", 401: "unauthorized", 403: "forbidden", 404: "not_found",
		405: "method_not_allowed", 409: "conflict", 413: "too_large", 422: "unprocessable", 429: "rate_limited",
		500: "internal", 501: "not_implemented", 502: "bad_gateway", 503: "unavailable", 418: "error", 410: "error"} {
		if got := DefaultCode(status); got != want {
			t.Errorf("DefaultCode(%d) = %q, want %q", status, got, want)
		}
	}
}

// An auth proxy that stops the request answers with its own page, not
// the API's JSON: that is its own error, not a JSON parse error.
func TestProxyInterception(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"a login page with 200", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<html>sign in</html>")
		}},
		{"a 401 page", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(401)
			io.WriteString(w, "<html>no</html>")
		}},
		{"a redirect to the login", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://auth.example.net/login?rd=secret", http.StatusFound)
		}},
		{"a 200 with no content type", func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = nil
			w.Write([]byte("plain"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTest(t, "tok", tc.h)
			_, err := c.Version(context.Background())
			var ie *InterceptedError
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v (%T), want *InterceptedError", err, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "auth proxy") || strings.Contains(msg, "secret") {
				t.Errorf("message %q: names the proxy and never repeats a query", msg)
			}
		})
	}
}

// A reverse proxy's own error page for a server that's down or
// restarting is that status, not an interception: a send retries it.
func TestProxyErrorPageIsTheStatus(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
			io.WriteString(w, "<html>bad gateway</html>")
		})
		_, err := c.Version(context.Background())
		var e *Error
		if !errors.As(err, &e) || e.Status != status || e.Code != DefaultCode(status) {
			t.Errorf("%d: err = %v (%T), want *Error with the status", status, err, err)
		}
		if !retryable(err) {
			t.Errorf("%d: not retryable", status)
		}
	}
}

// The client follows no redirect: an https:// server is never left for
// http://, and the token never goes to another host.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "" {
			t.Error("the token followed the redirect")
		}
	}))
	t.Cleanup(other.Close)
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v1/version", http.StatusTemporaryRedirect)
	})
	if _, err := c.Version(context.Background()); err == nil {
		t.Fatal("a redirect was followed")
	}
	if hits != 0 {
		t.Errorf("the redirect target got %d requests", hits)
	}
}

func TestOversizedResponseIsAnError(t *testing.T) {
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api_version":"`)
		io.WriteString(w, strings.Repeat("x", maxBody+1))
		io.WriteString(w, `"}`)
	})
	if _, err := c.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v, want a too-large error", err)
	}
}

func TestIdempotencyKeys(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		k := NewIdempotencyKey()
		if len(k) < 22 || len(k) > 255 || strings.ContainsAny(k, " \t\r\n") {
			t.Fatalf("key %q: want at least 128 bits, at most 255 chars, no space", k)
		}
		if seen[k] {
			t.Fatalf("key %q repeated", k)
		}
		seen[k] = true
	}
}

// Send retries a request that may or may not have reached the server
// with the same Idempotency-Key, so a retry can't send the message twice.
func TestSendRetriesWithTheSameKey(t *testing.T) {
	var keys []string
	var bodies []string
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(b))
		if r.Header.Get("Prefer") != "respond-async" {
			t.Errorf("Prefer = %q, want respond-async", r.Header.Get("Prefer"))
		}
		if len(keys) < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			io.WriteString(w, `{"error":"shutting down","code":"unavailable"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"dispatch_id":"d-1","conversation_id":"c-1","status":"queued"}`)
	})
	c.retryWait = time.Millisecond
	d, err := c.Send(context.Background(), SendRequest{ConversationID: "c-1", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if d.DispatchID != "d-1" || d.Status != DispatchQueued {
		t.Errorf("dispatch = %+v", d)
	}
	if len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("keys = %q, want one key on every attempt", keys)
	}
	if bodies[0] != bodies[2] || !strings.Contains(bodies[0], `"message":"hello"`) {
		t.Errorf("bodies = %q", bodies)
	}
}

func TestSendKeepsTheCallersKey(t *testing.T) {
	var key string
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		io.WriteString(w, `{"dispatch_id":"d-1","conversation_id":"c-1","status":"queued"}`)
	})
	if _, err := c.Send(context.Background(), SendRequest{Message: "hi", IdempotencyKey: "my-key"}); err != nil {
		t.Fatal(err)
	}
	if key != "my-key" {
		t.Errorf("Idempotency-Key = %q", key)
	}
}

// What the server answered is final: a 409 busy, a 422 or a 401 isn't
// retried.
func TestSendDoesNotRetryAnAnswer(t *testing.T) {
	for _, status := range []int{400, 401, 409, 413, 422} {
		var n int
		c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
			n++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"no","code":"x"}`)
		})
		c.retryWait = time.Millisecond
		if _, err := c.Send(context.Background(), SendRequest{Message: "hi"}); err == nil {
			t.Errorf("%d: no error", status)
		}
		if n != 1 {
			t.Errorf("%d: %d attempts, want 1", status, n)
		}
	}
}
