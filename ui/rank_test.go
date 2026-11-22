package ui

import (
	"slices"
	"testing"
	"time"

	"github.com/Loomux/tui/client"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func row(id, status string, updated time.Duration) client.ConversationSummary {
	return client.ConversationSummary{ConversationID: id, Status: status, UpdatedAt: t0.Add(-updated)}
}

func ids(rows []client.ConversationSummary) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ConversationID
	}
	return out
}

// Ranked like the web (src/lib/conversations.ts): needs_attention,
// awaiting_input and human_takeover first, then running, then the rest;
// newest first within a group, conversation_id as the tie-breaker.
func TestRankIsAttentionFirst(t *testing.T) {
	rows := []client.ConversationSummary{
		row("done-new", client.TaskCompleted, time.Minute),
		row("run-old", client.TaskRunning, time.Hour),
		row("failed", client.TaskFailed, 2*time.Minute),
		row("attention", client.TaskNeedsAttention, 3*time.Hour),
		row("run-new", client.TaskRunning, time.Second),
		row("awaiting", client.TaskAwaitingInput, 2*time.Hour),
		row("takeover", client.TaskHumanTakeover, 4*time.Hour),
		row("odd", "something_new", 0), // an unknown status ranks with the done ones
		row("tie-b", client.TaskCompleted, 5*time.Hour),
		row("tie-a", client.TaskCompleted, 5*time.Hour),
	}
	rank(rows)
	want := []string{"awaiting", "attention", "takeover", "run-new", "run-old", "odd", "done-new", "failed", "tie-a", "tie-b"}
	if got := ids(rows); !slices.Equal(got, want) {
		t.Errorf("rank = %v\nwant   %v", got, want)
	}
}

// A conversation waiting on a yes or no to an offer needs the user,
// whatever its latest task's status (S6's pending_confirmation_id).
func TestRankPendingOfferNeedsYou(t *testing.T) {
	offer := row("offer", client.TaskCompleted, 9*time.Hour)
	offer.PendingConfirmationID = "conf-1"
	rows := []client.ConversationSummary{row("run", client.TaskRunning, 0), offer}
	rank(rows)
	if got := ids(rows); !slices.Equal(got, []string{"offer", "run"}) {
		t.Errorf("rank = %v, want the offer first", got)
	}
	if groupOf(offer) != needsYou {
		t.Errorf("groupOf(offer) = %v", groupOf(offer))
	}
}

// tab cycles the list through needs you, running, done and all.
func TestViewCycle(t *testing.T) {
	v := allView
	var got []view
	for range 5 {
		v = v.next()
		got = append(got, v)
	}
	if want := []view{needsYouView, runningView, doneView, allView, needsYouView}; !slices.Equal(got, want) {
		t.Errorf("cycle = %v, want %v", got, want)
	}
	if !allView.shows(done) || !needsYouView.shows(needsYou) || needsYouView.shows(running) || !doneView.shows(done) || doneView.shows(running) {
		t.Error("views show the wrong groups")
	}
}

// The filter matches every word of the query, in any case, against what
// a row shows: preview, workspace, status and holder, and its id.
func TestMatches(t *testing.T) {
	r := row("conv-42", client.TaskNeedsAttention, 0)
	r.Preview = "Restart the API with kubectl"
	r.HeldBy = &client.Holder{ClientLabel: "laptop"}
	item := newItem(r, map[string]string{})
	item.workspace = "cc-hub"
	for q, want := range map[string]bool{
		"":                true,
		"  ":              true,
		"kubectl":         true,
		"KUBECTL":         true,
		"cc-hub":          true,
		"hub kubectl":     true, // every word, anywhere
		"hub nginx":       false,
		"laptop":          true,
		"held":            true,
		"conv-42":         true,
		"restart the api": true,
		"nginx":           false,
	} {
		if got := item.matches(q); got != want {
			t.Errorf("matches(%q) = %v, want %v", q, got, want)
		}
	}
}

// A row says who holds it, then a pending offer, then its status.
func TestStatusWords(t *testing.T) {
	held := row("h", client.TaskHumanTakeover, 0)
	held.HeldBy = &client.Holder{ClientKind: "tui", ClientLabel: "laptop"}
	kindOnly := row("k", client.TaskHumanTakeover, 0)
	kindOnly.HeldBy = &client.Holder{ClientKind: "tui"}
	offer := row("o", client.TaskCompleted, 0)
	offer.PendingConfirmationID = "c"
	cases := []struct {
		r            client.ConversationSummary
		glyph, label string
	}{
		{held, "⌨", "held: laptop"},
		{kindOnly, "⌨", "held: tui"},
		{offer, "?", "offer"},
		{row("a", client.TaskNeedsAttention, 0), "▲", "needs attention"},
		{row("b", client.TaskAwaitingInput, 0), "●", "awaiting input"},
		{row("c", client.TaskHumanTakeover, 0), "⌨", "taken over"},
		{row("d", client.TaskRunning, 0), "◐", "running"},
		{row("e", client.TaskCompleted, 0), "✓", "completed"},
		{row("f", client.TaskFailed, 0), "✗", "failed"},
		{row("g", "paused_for_\x1b[31mlunch", 0), "·", "paused for [31mlunch"},
	}
	for _, c := range cases {
		if g, l := status(c.r); g != c.glyph || l != c.label {
			t.Errorf("status(%s %q) = %q %q, want %q %q", c.r.ConversationID, c.r.Status, g, l, c.glyph, c.label)
		}
	}
}

// Rows name workspaces from GET /workspaces; until a name is known, the
// start of the id stands in.
func TestWorkspaceName(t *testing.T) {
	names := map[string]string{"4f0c6d2e-0000-4000-8000-000000000001": "cc-hub"}
	for id, want := range map[string]string{
		"4f0c6d2e-0000-4000-8000-000000000001": "cc-hub",
		"9a1b2c3d-0000-4000-8000-000000000002": "9a1b2c3d",
		"short":                                "short",
		"":                                     "—",
	} {
		if got := workspaceName(id, names); got != want {
			t.Errorf("workspaceName(%q) = %q, want %q", id, got, want)
		}
	}
}

// Text from the server is put on one line, and can't reach the terminal
// as a control sequence or reorder the row.
func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                        "plain",
		"two\nlines\r\n\tand  spaces ": "two lines and spaces",
		"\x1b[2J\x1b[Hcleared":         "[2J[Hcleared",
		"bell\a and \u009b csi":        "bell and csi", // C1 CSI
		"bad \xff byte":                "bad \ufffd byte",
		"abc\u202edcba":                "abcdcba",
		"  leading":                    "leading",
		"emoji 🧵 and ünïcödé stay":     "emoji 🧵 and ünïcödé stay",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

// A row's age is short: seconds, minutes, hours, then days.
func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute:                          "0s", // a clock ahead of ours
		0:                                     "0s",
		30 * time.Second:                      "30s",
		59*time.Second + 999*time.Millisecond: "59s",
		time.Minute:                           "1m",
		59 * time.Minute:                      "59m",
		time.Hour:                             "1h",
		23*time.Hour + 59*time.Minute:         "23h",
		24 * time.Hour:                        "1d",
		400 * 24 * time.Hour:                  "400d",
	} {
		if got := ago(t0, t0.Add(-d)); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

// Watchers are counted after the holder or status, so the filter finds
// them too.
func TestItemWatchers(t *testing.T) {
	r := row("w", client.TaskRunning, 0)
	r.Watchers = 2
	if it := newItem(r, nil); it.label != "running, 2 watching" || !it.matches("watching") {
		t.Errorf("label %q", it.label)
	}
}
