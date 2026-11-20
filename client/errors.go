package client

import (
	"errors"
	"fmt"
	"net/http"
)

// Codes of the server's error envelope, {error, code}: api/README.md,
// "Error codes". A code once given to a case doesn't change, and new
// ones may be added within v1.
const (
	// Each status's default, when a handler names no specific case.
	CodeInvalidRequest   = "invalid_request"    // 400
	CodeUnauthorized     = "unauthorized"       // 401
	CodeForbidden        = "forbidden"          // 403
	CodeNotFound         = "not_found"          // 404
	CodeMethodNotAllowed = "method_not_allowed" // 405
	CodeConflict         = "conflict"           // 409
	CodeTooLarge         = "too_large"          // 413
	CodeUnprocessable    = "unprocessable"      // 422
	CodeRateLimited      = "rate_limited"       // 429
	CodeInternal         = "internal"           // 500
	CodeNotImplemented   = "not_implemented"    // 501
	CodeBadGateway       = "bad_gateway"        // 502
	CodeUnavailable      = "unavailable"        // 503
	CodeError            = "error"              // any other status

	// Specific cases a client can act on.

	// CodeConversationBusy: POST /dispatch 409, a dispatch is in flight in
	// the conversation; Error.DispatchID names it.
	CodeConversationBusy = "conversation_busy"
	// CodeIdempotencyConflict: POST /dispatch 422, the Idempotency-Key was
	// used for a different request.
	CodeIdempotencyConflict = "idempotency_conflict"
	// CodeTaskHeld: takeover 409, another session holds the task;
	// Error.Hold is its hold. Force takes it.
	CodeTaskHeld = "task_held"
	// CodeTaskInactive: takeover 409, the task has ended and its pane
	// wasn't kept for a person.
	CodeTaskInactive = "task_inactive"
	// CodeUnsupportedMode: takeover 400, the server can't take a hold in
	// the mode asked for (watch, before the server has watch holds). No
	// hold was taken.
	CodeUnsupportedMode = "unsupported_mode"
	// CodeHoldEnded: heartbeat 410, the hold has ended; Error.EndedReason
	// says why and Error.Hold is the hold as it ended.
	CodeHoldEnded = "hold_ended"
	// CodeDetachFailed: release (Take back) or a forced takeover 502, the
	// holder's tmux clients couldn't be detached. Nothing changed: try
	// again.
	CodeDetachFailed = "detach_failed"
	// CodeUnsupportedAPIVersion: 404 for a path outside /api/v1.
	CodeUnsupportedAPIVersion = "unsupported_api_version"
)

// DefaultCode is the code the server gives status when it names no
// specific case, and the code an unknown code is treated as.
func DefaultCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeInvalidRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusConflict:
		return CodeConflict
	case http.StatusRequestEntityTooLarge:
		return CodeTooLarge
	case http.StatusUnprocessableEntity:
		return CodeUnprocessable
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusInternalServerError:
		return CodeInternal
	case http.StatusNotImplemented:
		return CodeNotImplemented
	case http.StatusBadGateway:
		return CodeBadGateway
	case http.StatusServiceUnavailable:
		return CodeUnavailable
	}
	return CodeError
}

// Error is an error the API answered with: its status and the {error,
// code} envelope, with the fields some codes carry.
type Error struct {
	Status int
	// Code is the envelope's code, or the status's default when the
	// response had none. It may be one this client doesn't know (a later
	// server's): Is treats that as the status's default.
	Code string
	// Message is the server's error text, to show as it is.
	Message string

	// DispatchID is the dispatch in flight (conversation_busy).
	DispatchID string
	// Hold is the task's hold: the other session's (task_held) or the
	// ended one (hold_ended).
	Hold *Hold
	// EndedReason is why the hold ended (hold_ended).
	EndedReason string
}

// Error returns the server's text, or the status when it gave none.
func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status))
}

// Is reports whether err is an API error with code: its own code, or its
// status's default, which every error of that status matches (a
// conversation_busy is also a conflict). An unknown code thus falls back
// to its status's handling.
func Is(err error, code string) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == code || DefaultCode(e.Status) == code
}

// Unauthorized reports whether err is the API's 401: the token is unknown,
// revoked or expired. A request made with the stored token deletes it and
// signs out (the design's "Error handling").
func Unauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// InterceptedError is a response that isn't the API's: an HTML page, a
// redirect, anything but JSON. In a deployment behind an auth proxy it is
// the proxy's login page or redirect, which means the token wasn't sent or
// the server URL is wrong.
type InterceptedError struct {
	Status      int
	ContentType string
	// Location is a redirect's target, scheme and host only (its path and
	// query may hold secrets).
	Location string
}

func (e *InterceptedError) Error() string {
	what := fmt.Sprintf("HTTP %d", e.Status)
	if e.Location != "" {
		what += ", a redirect to " + e.Location
	} else if e.ContentType != "" {
		what += ", " + e.ContentType
	}
	return "the request was stopped by the auth proxy (" + what + " instead of the API's JSON): is the token sent? Is the server URL right?"
}
