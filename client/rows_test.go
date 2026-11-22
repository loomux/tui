package client

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// A list row carries held_by and pending_confirmation_id (S6) and
// watchers (S10) once the server has them. The pinned server has none,
// so they are read when present and empty when not.
func TestConversationRowsCarryHoldsOffersAndWatchersWhenPresent(t *testing.T) {
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"conversations":[
			{"conversation_id":"held","status":"human_takeover","updated_at":"2026-11-20T10:00:00Z",
			 "held_by":{"client_kind":"tui","client_label":"laptop","since":"2026-11-20T09:58:00Z"},"watchers":2},
			{"conversation_id":"offer","status":"completed","updated_at":"2026-11-20T09:00:00Z",
			 "pending_confirmation_id":"conf-1"},
			{"conversation_id":"plain","status":"running","updated_at":"2026-11-20T08:00:00Z"}
		],"has_more":false}`)
	})
	page, err := c.Conversations(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	held, offer, plain := page.Conversations[0], page.Conversations[1], page.Conversations[2]
	if held.HeldBy == nil || held.HeldBy.ClientLabel != "laptop" || held.HeldBy.ClientKind != "tui" ||
		!held.HeldBy.Since.Equal(time.Date(2026, 11, 20, 9, 58, 0, 0, time.UTC)) || held.Watchers != 2 {
		t.Errorf("held row = %+v (held_by %+v)", held, held.HeldBy)
	}
	if offer.PendingConfirmationID != "conf-1" || offer.HeldBy != nil {
		t.Errorf("offer row = %+v", offer)
	}
	if plain.HeldBy != nil || plain.PendingConfirmationID != "" || plain.Watchers != 0 {
		t.Errorf("plain row = %+v", plain)
	}
}

func TestWorkspaces(t *testing.T) {
	var path string
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"workspaces":[{"id":"ws-1","name":"cc-hub","status":"idle"}]}`)
	})
	list, err := c.Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/workspaces" || len(list) != 1 || list[0].ID != "ws-1" || list[0].Name != "cc-hub" {
		t.Errorf("GET %s: %+v", path, list)
	}
}

// A conversation's task carries its active hold (S6) once the server
// has it: a drive hold, or null. The pinned server sends neither, so it
// is read when present and nil when not.
func TestConversationTasksCarryTheirHoldWhenPresent(t *testing.T) {
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"conversation_id":"c-1","messages":[],"tasks":[
			{"id":"held","status":"failed","error_class":"login_required",
			 "hold":{"id":"h-1","task_id":"held","mode":"drive","client_kind":"tui","client_label":"laptop",
			         "acquired_at":"2026-11-20T09:58:00Z"}},
			{"id":"free","status":"awaiting_input","hold":null},
			{"id":"old","status":"human_takeover"}
		],"dispatches":[],"confirmations":[],"has_more":false}`)
	})
	conv, err := c.Conversation(context.Background(), "c-1", Page{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	held, free, old := conv.Tasks[0], conv.Tasks[1], conv.Tasks[2]
	if held.Hold == nil || held.Hold.ID != "h-1" || held.Hold.Mode != ModeDrive || held.Hold.ClientLabel != "laptop" {
		t.Errorf("held task's hold = %+v", held.Hold)
	}
	if free.Hold != nil || old.Hold != nil {
		t.Errorf("unheld tasks' holds = %+v, %+v; want nil", free.Hold, old.Hold)
	}
}
