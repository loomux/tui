// Package client is the typed client of the Loomux server's API v1 (the
// design in loomux/server, docs/design/tui-client-design.md,
// "Repository"): requests, the {error, code} envelope, the conversation
// stream (Server-Sent Events) and idempotent sends.
//
// Its structs hold only the fields the client reads. Every route, field,
// status and header it relies on is listed in testdata/uses.txt, which a
// test checks against the pinned server's contract file, and the calls
// are tested against a real server through the server's api/apitest.
//
// A Client never logs, prints or puts its token in an error.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Loomux/tui/config"
	"github.com/Loomux/tui/internal/version"
)

// APIVersion is the API version this client speaks: GET /version's
// api_version. A server answering another one gets a persistent warning
// (the design's "Versions and compatibility").
const APIVersion = "v1"

// maxBody caps a JSON response. The largest is a conversation page.
const maxBody = 16 << 20

// Client calls one server's API v1 with one token. It is safe for
// concurrent use.
type Client struct {
	server string
	token  string
	hc     *http.Client
	ua     string

	// Send's retries of a request that may not have arrived.
	retries   int
	retryWait time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient makes the client send its requests through hc's
// transport. Its redirect policy is replaced: the client follows none.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		cp := *hc
		c.hc = &cp
	}
}

// New returns a client of server, a root URL the HTTPS-only rule allows
// (config.CheckServer), sending token as its bearer token. An empty token
// sends none: only GET /version answers that.
func New(server, token string, opts ...Option) (*Client, error) {
	root, err := config.CheckServer(server)
	if err != nil {
		return nil, err
	}
	c := &Client{
		server:    root,
		token:     token,
		hc:        &http.Client{},
		ua:        "loomux/" + version.Client(),
		retries:   2,
		retryWait: 500 * time.Millisecond,
	}
	for _, o := range opts {
		o(c)
	}
	// No redirect is followed: the API answers none, an https:// server
	// is never left for anything else (T1's rule), and the token never
	// goes to another host. A redirect is an auth proxy's, reported as
	// an InterceptedError.
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// Server is the server's root URL, normalised.
func (c *Client) Server() string { return c.server }

// request is one API call.
type request struct {
	method string
	path   string // under /api/v1, already escaped
	query  url.Values
	body   any
	header http.Header
	// ok lists the statuses that are a success; out receives the body.
	ok  []int
	out any
}

// seg escapes one path segment, an id.
func seg(id string) string { return url.PathEscape(id) }

func (c *Client) newRequest(ctx context.Context, r request) (*http.Request, error) {
	u := c.server + "/api/v1" + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", c.ua)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// do sends r and decodes the answer: into r.out on a status in r.ok, else
// into an *Error or an *InterceptedError.
func (c *Client) do(ctx context.Context, r request) error {
	req, err := c.newRequest(ctx, r)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, r.ok, r.out)
}

func decode(resp *http.Response, ok []int, out any) error {
	success := false
	for _, s := range ok {
		success = success || resp.StatusCode == s
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return intercepted(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("reading the response: %w", err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("the response is too large (over %d MiB)", maxBody>>20)
	}
	if success {
		if out == nil {
			return nil
		}
		if !isJSON(resp) {
			return intercepted(resp)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("the server's response: %w", err)
		}
		return nil
	}
	if len(body) > 0 && !isJSON(resp) {
		// A 5xx page is a reverse proxy's for a server that's down or
		// restarting: the status says it. Anything else is a proxy that
		// stopped the request.
		if resp.StatusCode >= 500 {
			return apiError(resp.StatusCode, nil)
		}
		return intercepted(resp)
	}
	return apiError(resp.StatusCode, body)
}

// apiError reads the {error, code} envelope of a failed request.
func apiError(status int, body []byte) *Error {
	var env struct {
		Error       string `json:"error"`
		Code        string `json:"code"`
		DispatchID  string `json:"dispatch_id"`
		Hold        *Hold  `json:"hold"`
		EndedReason string `json:"ended_reason"`
	}
	_ = json.Unmarshal(body, &env) // not the envelope: the status alone
	e := &Error{Status: status, Code: env.Code, Message: env.Error,
		DispatchID: env.DispatchID, Hold: env.Hold, EndedReason: env.EndedReason}
	if e.Code == "" {
		e.Code = DefaultCode(status)
	}
	return e
}

func isJSON(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

func intercepted(resp *http.Response) error {
	e := &InterceptedError{Status: resp.StatusCode}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
		e.ContentType = mt
	}
	if loc, err := resp.Location(); err == nil && loc.Host != "" {
		e.Location = loc.Scheme + "://" + loc.Host
	}
	return e
}

// retryable reports whether a request that failed with err may not have
// reached the server, or reached one that couldn't take it (shutting
// down): sending it again with the same Idempotency-Key is safe.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Status == http.StatusBadGateway || e.Status == http.StatusServiceUnavailable || e.Status == http.StatusGatewayTimeout
	}
	var ie *InterceptedError
	return !errors.As(err, &ie)
}
