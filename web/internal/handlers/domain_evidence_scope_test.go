package handlers

import (
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// The approvals queue is filtered with the rest of the page. A view
// headed "showing only what A proposed" that lists B's pending work
// contradicts itself — in the tab badge as much as in the panel.
func TestApprovalsQueueFollowsTheActorFilter(t *testing.T) {
	t.Parallel()
	queue := []apiclient.PendingApproval{
		{ID: "ap-1", RequestedBy: "refund-agent-eu", ActionName: "ISSUE_REFUND"},
		{ID: "ap-2", RequestedBy: "some-other-agent", ActionName: "ISSUE_REFUND"},
		{ID: "ap-3", RequestedBy: "refund-agent-eu", ActionName: "ISSUE_CREDIT"},
	}
	mine, elsewhere := splitApprovalsByActor(queue, "refund-agent-eu")
	if len(mine) != 2 {
		t.Errorf("kept %d approvals, want the holder's two", len(mine))
	}
	for _, a := range mine {
		if a.RequestedBy != "refund-agent-eu" {
			t.Errorf("leaked %s's approval into a filtered queue", a.RequestedBy)
		}
	}
	// Counted, never silently dropped: work waiting on a reviewer must
	// not vanish because of a filter they may not have set themselves.
	if elsewhere != 1 {
		t.Errorf("elsewhere = %d, want the one being kept off the screen", elsewhere)
	}
}

func TestUnfilteredQueueIsUntouched(t *testing.T) {
	t.Parallel()
	queue := []apiclient.PendingApproval{{ID: "ap-1", RequestedBy: "a"}, {ID: "ap-2", RequestedBy: "b"}}
	mine, elsewhere := splitApprovalsByActor(queue, "")
	if len(mine) != 2 || elsewhere != 0 {
		t.Errorf("unfiltered queue changed: %d kept, %d elsewhere", len(mine), elsewhere)
	}
}
