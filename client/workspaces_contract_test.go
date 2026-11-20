package client_test

import (
	"testing"

	"github.com/Loomux/server/api/apitest"
)

// The list names a row's workspace: GET /workspaces maps a row's
// workspace_id to its name.
func TestContractWorkspaces(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	ws := srv.SeedWorkspace("cc-hub", srv.SeedTarget("lab"))
	conv := srv.SeedConversation("", "hello", "hi")
	list, err := c.Workspaces(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, w := range list {
		names[w.ID] = w.Name
	}
	if names[ws.ID] != "cc-hub" {
		t.Errorf("workspaces %+v lack %s (cc-hub)", list, ws.ID)
	}
	page, err := c.Conversations(ctx(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Conversations {
		if row.ConversationID == conv.ConversationID && names[row.WorkspaceID] == "" {
			t.Errorf("row %+v: workspace %q isn't in GET /workspaces %+v", row, row.WorkspaceID, list)
		}
	}
}
