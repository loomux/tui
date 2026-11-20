package client

import (
	"context"
	"net/http"
)

// Workspace is one row of GET /workspaces: the list names a row's
// workspace_id by it.
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Workspaces is GET /workspaces: every workspace.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	var out struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/workspaces", ok: []int{200}, out: &out})
	if err != nil {
		return nil, err
	}
	return out.Workspaces, nil
}
