package agentdrafts_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/agentdrafts"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

// The Postgres store moves a draft through its life with compare-and-set:
// confirm needs the version and an unexpired draft, a confirming draft
// can't be claimed or discarded until it finishes, a claim needs the
// secret's hash, each acts once, and a claim whose key failed can be
// undone.
func TestPostgresDraftLifecycle(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.agent_drafts WHERE tenant_id = 'pg-drafts'`)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ('pg-drafts', 'pg-drafts', 'active') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	s := agentdrafts.NewPostgresStore(pool)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cards := json.RawMessage(`[{"id":"rae-refund","subject":"rae"}]`)
	secret, hash := agentdrafts.NewClaimSecret()
	d := agentdrafts.D{ID: "drf_pg_1", TenantID: "pg-drafts", AgentID: "rae", Label: "Rae", Cards: cards,
		Version: agentdrafts.VersionOf("rae", "Rae", cards), Status: agentdrafts.Draft, CreatedBy: "setup-agent",
		CreatedAt: now, ExpiresAt: now.Add(agentdrafts.DefaultTTL), ClaimHash: hash}
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, d.ID, hash, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("claim before confirm: %v", err)
	}
	if _, err := s.Confirm(ctx, "pg-drafts", d.ID, "stale", "alice", "admin", nil, now); !errors.Is(err, agentdrafts.ErrVersion) {
		t.Errorf("stale confirm: %v", err)
	}
	got, err := s.Confirm(ctx, "pg-drafts", d.ID, d.Version, "alice", "admin", []string{"rae-refund"}, now)
	if err != nil || got.Status != agentdrafts.Confirming || got.ConfirmedBy != "alice" || got.ConfirmedRole != "admin" || len(got.CardIDs) != 1 {
		t.Fatalf("confirm: %+v %v", got, err)
	}
	if _, err := s.Confirm(ctx, "pg-drafts", d.ID, d.Version, "alice", "admin", nil, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("second confirm: %v", err)
	}
	if _, err := s.Claim(ctx, d.ID, hash, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("claim while confirming: %v", err)
	}
	if _, err := s.Discard(ctx, "pg-drafts", d.ID); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("discard while confirming: %v", err)
	}
	if fin, err := s.Finish(ctx, "pg-drafts", d.ID); err != nil || fin.Status != agentdrafts.Confirmed {
		t.Fatalf("finish: %+v %v", fin, err)
	}
	if _, err := s.Finish(ctx, "pg-drafts", d.ID); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("second finish: %v", err)
	}
	if _, err := s.Claim(ctx, d.ID, agentdrafts.HashClaim("kiff_claim_wrong"), now); !errors.Is(err, agentdrafts.ErrNotFound) {
		t.Errorf("wrong secret: %v", err)
	}
	c, err := s.Claim(ctx, d.ID, agentdrafts.HashClaim(secret), now)
	if err != nil || c.Status != agentdrafts.Claimed || len(c.CardIDs) != 1 {
		t.Fatalf("claim: %+v %v", c, err)
	}
	if err := s.Unclaim(ctx, "pg-drafts", d.ID); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	if _, err := s.Claim(ctx, d.ID, hash, now); err != nil {
		t.Fatalf("claim again: %v", err)
	}
	if err := s.SetKey(ctx, "pg-drafts", d.ID, "key_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, d.ID, hash, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("third claim: %v", err)
	}
	if _, err := s.Discard(ctx, "pg-drafts", d.ID); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("discard a claimed draft: %v", err)
	}

	e := d
	e.ID, e.ExpiresAt = "drf_pg_2", now.Add(time.Hour)
	if err := s.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ctx, "pg-drafts", e.ID, e.Version, "alice", "admin", nil, now.Add(2*time.Hour)); !errors.Is(err, agentdrafts.ErrExpired) {
		t.Errorf("confirm after expiry: %v", err)
	}
	if was, err := s.Discard(ctx, "pg-drafts", e.ID); err != nil || was.Status != agentdrafts.Draft {
		t.Errorf("discard: %+v %v", was, err)
	}
	if got, _ := s.Get(ctx, "pg-drafts", e.ID); got.Status != agentdrafts.Discarded {
		t.Errorf("a draft discards at once: %s", got.Status)
	}

	// A failed confirmation rolls back, and a discarded confirmed draft
	// discards, until Settle; the sweep finds both, and a confirmation
	// left confirming once it is stuck.
	for _, id := range []string{"drf_pg_3", "drf_pg_4", "drf_pg_5"} {
		f := d
		f.ID = id
		if err := s.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Confirm(ctx, "pg-drafts", id, f.Version, "alice", "admin", []string{id + "-card"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if rb, err := s.Fail(ctx, "pg-drafts", "drf_pg_3", "a Card could not be issued"); err != nil || rb.Status != agentdrafts.RollingBack || rb.Reason == "" {
		t.Fatalf("fail: %+v %v", rb, err)
	}
	if _, err := s.Claim(ctx, "drf_pg_3", hash, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("claim while rolling back: %v", err)
	}
	if _, err := s.Finish(ctx, "pg-drafts", "drf_pg_4"); err != nil {
		t.Fatal(err)
	}
	for range 2 { // a retried discard returns it again
		if was, err := s.Discard(ctx, "pg-drafts", "drf_pg_4"); err != nil || len(was.CardIDs) != 1 {
			t.Fatalf("discard a confirmed draft: %+v %v", was, err)
		}
	}
	if got, _ := s.Get(ctx, "pg-drafts", "drf_pg_4"); got.Status != agentdrafts.Discarding {
		t.Errorf("discarding: %s", got.Status)
	}
	unsettled, err := s.Unsettled(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]agentdrafts.Status{}
	for _, u := range unsettled {
		if u.TenantID == "pg-drafts" {
			seen[u.ID] = u.Status
		}
	}
	if seen["drf_pg_3"] != agentdrafts.RollingBack || seen["drf_pg_4"] != agentdrafts.Discarding || seen["drf_pg_5"] != agentdrafts.Confirming || len(seen) != 3 {
		t.Errorf("unsettled = %v", seen)
	}
	if early, _ := s.Unsettled(ctx, now); len(early) > 0 {
		for _, u := range early {
			if u.ID == "drf_pg_5" {
				t.Error("a confirmation not yet stuck was listed")
			}
		}
	}
	for id, want := range map[string]agentdrafts.Status{"drf_pg_3": agentdrafts.Failed, "drf_pg_4": agentdrafts.Discarded} {
		if err := s.Settle(ctx, "pg-drafts", id); err != nil {
			t.Fatalf("settle %s: %v", id, err)
		}
		if got, _ := s.Get(ctx, "pg-drafts", id); got.Status != want {
			t.Errorf("%s settled to %s, want %s", id, got.Status, want)
		}
	}
	if err := s.Settle(ctx, "pg-drafts", "drf_pg_5"); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("settle a confirming draft: %v", err)
	}
}

// A revision draft (kiff/tafs#107) keeps what it revises, and never gives a
// key or is discarded once confirmed: its Cards are the agent's own.
func TestPostgresRevisionDraft(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.agent_drafts WHERE tenant_id = 'pg-drafts-rev'`)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ('pg-drafts-rev', 'pg-drafts-rev', 'active') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	s := agentdrafts.NewPostgresStore(pool)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cards := json.RawMessage(`[{"id":"rae-refund","subject":"rae"}]`)
	_, hash := agentdrafts.NewClaimSecret()
	d := agentdrafts.D{ID: "drf_pg_rev", TenantID: "pg-drafts-rev", AgentID: "rae", Cards: cards,
		Version: agentdrafts.VersionOf("rae", "", cards), Status: agentdrafts.Draft, CreatedAt: now,
		ExpiresAt: now.Add(agentdrafts.DefaultTTL), ClaimHash: hash,
		Revision: true, Revises: []agentdrafts.Revised{{ID: "rae-refund", Revision: 3}}, Retires: []agentdrafts.Revised{{ID: "rae-read", Revision: 2}}, RetireRest: true}
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "pg-drafts-rev", d.ID)
	if err != nil || !got.Revision || len(got.Revises) != 1 || got.Revises[0] != (agentdrafts.Revised{ID: "rae-refund", Revision: 3}) || !got.RevisesCard("rae-refund") ||
		len(got.Retires) != 1 || got.Retires[0] != (agentdrafts.Revised{ID: "rae-read", Revision: 2}) || !got.RetiresCard("rae-read") || got.RetiresCard("rae-refund") || !got.RetireRest {
		t.Fatalf("read back = %+v, %v", got, err)
	}
	if _, err := s.Confirm(ctx, "pg-drafts-rev", d.ID, d.Version, "alice", "admin", []string{"rae-refund"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(ctx, "pg-drafts-rev", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, d.ID, hash, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("a revision draft claimed: %v", err)
	}
	if _, err := s.Discard(ctx, "pg-drafts-rev", d.ID); !errors.Is(err, agentdrafts.ErrState) {
		t.Errorf("a confirmed revision discarded: %v", err)
	}
}

// A trial (kiff/tafs#173) in both stores: only a revision draft of the
// version shown is tried; ending it returns it to draft, recording how,
// and it then waits as a fresh draft would; installing a draft being
// tried confirms it and records that its trial ended by install.
func TestTrialInBothStores(t *testing.T) {
	t.Run("memory", func(t *testing.T) { exerciseTrial(t, agentdrafts.NewMemoryStore(), "t-mem") })
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := agentdrafts.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM cloud_meta.agent_drafts WHERE tenant_id = 'pg-trials'`)
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants (id, slug, status) VALUES ('pg-trials', 'pg-trials', 'active') ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	t.Run("postgres", func(t *testing.T) { exerciseTrial(t, agentdrafts.NewPostgresStore(pool), "pg-trials") })
}

func exerciseTrial(t *testing.T, s agentdrafts.Store, tnt string) {
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	draft := func(id string, revision bool) agentdrafts.D {
		cards := json.RawMessage(`[{"id":"rae-refund","subject":"rae"}]`)
		d := agentdrafts.D{ID: id, TenantID: tnt, AgentID: "rae", Cards: cards, Version: "v-" + id, Status: agentdrafts.Draft,
			CreatedAt: now, ExpiresAt: now.Add(agentdrafts.DefaultTTL), ClaimHash: "h-" + id, Revision: revision}
		if err := s.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	until := now.Add(agentdrafts.TrialTTL)
	trial := agentdrafts.Trial{CardIDs: []string{"rae-refund~trial-a"}, StartedAt: now, ExpiresAt: until}
	if _, err := s.StartTrial(ctx, tnt, draft("drf_new", false).ID, "v-drf_new", trial, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Fatalf("trying a new agent's draft: %v", err)
	}
	d := draft("drf_v2", true)
	if _, err := s.StartTrial(ctx, tnt, d.ID, "stale", trial, now); !errors.Is(err, agentdrafts.ErrVersion) {
		t.Fatalf("trying another version: %v", err)
	}
	got, err := s.StartTrial(ctx, tnt, d.ID, d.Version, trial, now)
	if err != nil || got.Status != agentdrafts.Trying || got.Trial == nil || got.Trial.CardIDs[0] != "rae-refund~trial-a" ||
		!got.ExpiresAt.Equal(until) || !got.TrialOpen(now) {
		t.Fatalf("start: %+v %v", got, err)
	}
	if _, err := s.StartTrial(ctx, tnt, d.ID, d.Version, trial, now); !errors.Is(err, agentdrafts.ErrState) {
		t.Fatalf("start twice: %v", err)
	}
	if open, err := s.TryingFor(ctx, tnt, "rae"); err != nil || len(open) != 1 || open[0].ID != d.ID {
		t.Fatalf("trying for rae: %+v %v", open, err)
	}
	if _, err := s.Discard(ctx, tnt, d.ID); !errors.Is(err, agentdrafts.ErrState) {
		t.Fatalf("discard while trying: %v", err)
	}
	back := now.Add(time.Hour)
	got, err = s.EndTrial(ctx, tnt, d.ID, agentdrafts.EndedBack, back)
	if err != nil || got.Status != agentdrafts.Draft || got.Trial.EndedBy != agentdrafts.EndedBack || !got.Trial.EndedAt.Equal(back) ||
		!got.ExpiresAt.Equal(back.Add(agentdrafts.DefaultTTL)) {
		t.Fatalf("end: %+v %v", got, err)
	}
	if at, by, ended := got.TrialEnd(back); !ended || by != agentdrafts.EndedBack || !at.Equal(back) {
		t.Fatalf("trial end: %v %s %v", at, by, ended)
	}
	if _, err := s.EndTrial(ctx, tnt, d.ID, agentdrafts.EndedBack, back); !errors.Is(err, agentdrafts.ErrState) {
		t.Fatalf("end twice: %v", err)
	}
	// Tried again, then installed.
	again := agentdrafts.Trial{CardIDs: []string{"rae-refund~trial-b"}, StartedAt: back, ExpiresAt: back.Add(agentdrafts.TrialTTL)}
	if _, err := s.StartTrial(ctx, tnt, d.ID, d.Version, again, back); err != nil {
		t.Fatalf("try again: %v", err)
	}
	install := back.Add(time.Hour)
	got, err = s.Confirm(ctx, tnt, d.ID, d.Version, "alice", "admin", []string{"rae-refund"}, install)
	if err != nil || got.Status != agentdrafts.Confirming || got.Trial == nil || got.Trial.EndedBy != agentdrafts.EndedInstall ||
		got.Trial.CardIDs[0] != "rae-refund~trial-b" || !got.Trial.EndedAt.Equal(install) {
		t.Fatalf("install: %+v %v", got, err)
	}
	if open, _ := s.TryingFor(ctx, tnt, "rae"); len(open) != 0 {
		t.Fatalf("still trying after install: %+v", open)
	}
}
