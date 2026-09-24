package herdr

import (
	"context"
)

// GetAgent returns the agent hosted by paneID (or by agent name).
func (c *Client) GetAgent(ctx context.Context, paneID string) (AgentInfo, error) {
	var res struct {
		Agent AgentInfo `json:"agent"`
	}
	err := c.Call(ctx, "agent.get", map[string]any{"target": paneID}, &res)
	return res.Agent, err
}
