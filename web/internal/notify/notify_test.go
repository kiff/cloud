package notify_test

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/notify"
)

func TestCompose_ApprovalsAreActionableAndCounted(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	approvals := []apiclient.PendingApproval{
		{ID: "a1", ActionName: "refund_order", EntityID: "order_731", EntityType: "Order", RequestedBy: "refund-agent", CreatedAt: now.Add(-8 * time.Minute)},
		{ID: "a2", ActionName: "issue_credit", EntityID: "acct_9", EntityType: "Account", RequestedBy: "credit-agent", CreatedAt: now.Add(-40 * time.Minute)},
	}

	sum := notify.Compose(approvals, nil, nil, now)

	if sum.Count != 2 {
		t.Fatalf("badge count = %d, want 2 (both approvals actionable)", sum.Count)
	}
	if len(sum.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(sum.Items))
	}
	// Newest first.
	if sum.Items[0].Title != "refund_order is waiting for approval" {
		t.Errorf("first item title = %q", sum.Items[0].Title)
	}
	if sum.Items[0].TimeLabel != "8m" {
		t.Errorf("time label = %q, want 8m", sum.Items[0].TimeLabel)
	}
	if !sum.Items[0].Actionable || sum.Items[0].Kind != notify.KindApproval {
		t.Errorf("approval should be actionable + KindApproval: %+v", sum.Items[0])
	}
	if sum.Items[0].Href != "/dashboard/activity?tab=approvals#ap-a1" {
		t.Errorf("approval href = %q (want deep link to the specific approval)", sum.Items[0].Href)
	}
}

func TestCompose_IdleRuntimeIsInformationalNotCounted(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	conns := []apiclient.GuardConnection{
		// Idle: last seen 2 days ago → surfaced, not counted.
		{AgentID: "support-agent", Workflow: "close-ticket", Environment: "production", LastSeenAt: now.Add(-48 * time.Hour)},
		// Live: last seen 10 min ago → not surfaced at all.
		{AgentID: "refund-agent", Workflow: "refund", LastSeenAt: now.Add(-10 * time.Minute)},
	}

	sum := notify.Compose(nil, conns, nil, now)

	if sum.Count != 0 {
		t.Fatalf("idle runtimes must not drive the badge: count = %d, want 0", sum.Count)
	}
	if len(sum.Items) != 1 {
		t.Fatalf("only the idle runtime should surface: items = %d, want 1", len(sum.Items))
	}
	it := sum.Items[0]
	if it.Kind != notify.KindRuntime || it.Actionable {
		t.Errorf("idle runtime should be KindRuntime + non-actionable: %+v", it)
	}
	if it.Title != "close-ticket has gone quiet" {
		t.Errorf("title = %q", it.Title)
	}
}

func TestCompose_OrdersNewestFirstAcrossKinds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	approvals := []apiclient.PendingApproval{
		{ID: "a1", ActionName: "refund_order", CreatedAt: now.Add(-3 * time.Hour)},
	}
	conns := []apiclient.GuardConnection{
		{AgentID: "agent-x", LastSeenAt: now.Add(-26 * time.Hour)}, // idle, but more recent than... no
	}

	sum := notify.Compose(approvals, conns, nil, now)
	if len(sum.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(sum.Items))
	}
	// Approval (-3h) is more recent than the idle runtime (-26h) → first.
	if sum.Items[0].Kind != notify.KindApproval {
		t.Errorf("expected the newer approval first, got %+v", sum.Items[0])
	}
}

func TestCompose_Empty(t *testing.T) {
	t.Parallel()
	sum := notify.Compose(nil, nil, nil, time.Now())
	if sum.Count != 0 || len(sum.Items) != 0 {
		t.Fatalf("empty inputs → empty summary, got %+v", sum)
	}
}

// A muted rule contributes nothing to the feed; the other kind still
// surfaces. Absent rules default to on.
func TestCompose_PrefsMuteAKind(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	approvals := []apiclient.PendingApproval{
		{ID: "a1", ActionName: "refund_order", CreatedAt: now.Add(-5 * time.Minute)},
	}
	conns := []apiclient.GuardConnection{
		{AgentID: "idle-agent", LastSeenAt: now.Add(-48 * time.Hour)},
	}

	// Mute runtime-idle only: the approval survives, the idle runtime is gone.
	sum := notify.Compose(approvals, conns, notify.Prefs{notify.RuleRuntimeIdle: false}, now)
	if len(sum.Items) != 1 || sum.Items[0].Kind != notify.KindApproval {
		t.Fatalf("muting runtime_idle should leave only the approval, got %+v", sum.Items)
	}

	// Mute approvals: nothing actionable, only the idle runtime (info).
	sum = notify.Compose(approvals, conns, notify.Prefs{notify.RuleApproval: false}, now)
	if sum.Count != 0 {
		t.Errorf("muting approvals should zero the badge, got %d", sum.Count)
	}
	if len(sum.Items) != 1 || sum.Items[0].Kind != notify.KindRuntime {
		t.Errorf("muting approvals should leave only the idle runtime, got %+v", sum.Items)
	}
}

func TestSummary_UnclearedHidesItemsUpToClearedAt(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	sum := notify.Summary{
		Count: 2,
		Items: []notify.Item{
			{Title: "new", Actionable: true, At: now.Add(time.Minute), Since: now.Add(time.Minute)},
			{Title: "old", Actionable: true, At: now.Add(-time.Hour), Since: now.Add(-time.Hour)},
			{Title: "idle", At: now.Add(-48 * time.Hour), Since: now.Add(-24 * time.Hour)},
		},
	}
	if got := sum.Uncleared(); len(got.Items) != 3 || got.Count != 2 {
		t.Fatalf("never cleared: got %d items, count %d", len(got.Items), got.Count)
	}
	sum.ClearedAt = now
	got := sum.Uncleared()
	if len(got.Items) != 1 || got.Items[0].Title != "new" || got.Count != 1 {
		t.Fatalf("cleared: got %+v", got)
	}
}

// A runtime that is still healthy when the user clears must still show
// up in the bell once it goes idle, even though its At (last traffic)
// predates the clear.
func TestUncleared_RuntimeGoingIdleAfterClearStillShows(t *testing.T) {
	clearedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	lastSeen := clearedAt.Add(-time.Hour) // healthy at clear time
	conns := []apiclient.GuardConnection{{AgentID: "refund-agent", LastSeenAt: lastSeen}}

	atClear := notify.Compose(nil, conns, nil, clearedAt)
	if len(atClear.Items) != 0 {
		t.Fatalf("runtime should be healthy at clear time, got %d items", len(atClear.Items))
	}

	nextDay := notify.Compose(nil, conns, nil, clearedAt.Add(24*time.Hour))
	nextDay.ClearedAt = clearedAt
	if got := nextDay.Uncleared(); len(got.Items) != 1 {
		t.Fatalf("runtime that went idle after the clear is hidden: got %d items", len(got.Items))
	}

	// Cleared once it is already idle: stays hidden for that episode.
	nextDay.ClearedAt = clearedAt.Add(24 * time.Hour)
	if got := nextDay.Uncleared(); len(got.Items) != 0 {
		t.Fatalf("runtime cleared while idle should be hidden, got %d items", len(got.Items))
	}
}
