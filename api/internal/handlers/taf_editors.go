package handlers

import (
	"context"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// editorsOwnTafs is the refusal when an editor changes a taf someone else
// made.
const editorsOwnTafs = "an editor changes only the tafs they made"

// mayChangeTafs reports whether id may change what concerns these agents:
// an editor changes only the tafs they made. Admins,
// account keys and agents that aren't tafs are unaffected, and so is an
// agent the account has no record of. With no agent store wired, nothing
// is narrowed.
func mayChangeTafs(ctx context.Context, store agents.Store, id auth.Identity, tenantID string, agentIDs ...string) (bool, error) {
	if store == nil || id.MemberRole != membership.RoleEditor {
		return true, nil
	}
	list, err := store.List(ctx, tenantID)
	if err != nil {
		return false, err
	}
	made := make(map[string]agents.Agent, len(list))
	for _, a := range list {
		made[a.ID] = a
	}
	for _, agentID := range agentIDs {
		if a, ok := made[agentID]; ok && a.Kind == agents.KindTaf && a.CreatedBy != id.Subject {
			return false, nil
		}
	}
	return true, nil
}
