package mandate_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

func effectEvidence(tenant, prop string, at time.Time) mandate.NoEffectEvidence {
	r := refund(100)
	return mandate.NoEffectEvidence{TenantID: tenant, ProposalID: prop, Subject: r.Subject, Action: r.Action, At: at, Source: "gateway:terminal-no-effect-v1", EvidenceID: "outcome-" + prop, Reason: "provider refused before any effect"}
}

func testMixedReleases(t *testing.T, s mandate.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	m := homeCard("mixed", 50000, 500000)
	m.TenantID = tenant
	q := mandate.Quantity{Kind: mandate.KindCount}
	m.Aggregates = append(m.Aggregates, mandate.Aggregate{Quantity: q, Limit: 10, Window: mandate.WindowCalendarDay},
		mandate.Aggregate{Quantity: m.Aggregates[0].Quantity, Limit: 500000, Window: mandate.WindowCalendarDay, Measure: mandate.MeasureAttempts},
		mandate.Aggregate{Quantity: q, Limit: 10, Window: mandate.WindowRollingHour, Measure: mandate.MeasureEffects})
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	if d, err := a.Authorize(ctx, tenant, "effect-op", refund(40000)); err != nil || !d.Allowed() {
		t.Fatal(d, err)
	}
	rs := s.(mandate.EffectReleaseStore)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rs.ReleaseEffects(ctx, effectEvidence(tenant, "effect-op", base.Add(time.Second))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i, a := range m.Aggregates {
		b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, a, base.Add(2*time.Second), "")
		want := int64(0)
		if a.EffectiveMeasure() == mandate.MeasureAttempts {
			want = mandate.DrawFor(a.Quantity, refund(40000).Parameters)
		}
		if err != nil || b.Net != want || b.Gross != mandate.DrawFor(a.Quantity, refund(40000).Parameters) {
			t.Fatalf("limit %d: %+v %v want=%d", i, b, err, want)
		}
	}
	if d, err := a.Authorize(ctx, tenant, "effect-op", refund(40000)); err != nil || d.Outcome != mandate.OutcomeProposalReused {
		t.Fatalf("released operation reopened: %+v %v", d, err)
	}
	rows, err := rs.EffectReleases(ctx, tenant, m.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("release rows %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.EvidenceID != "outcome-effect-op" || row.Reason == "" {
			t.Fatal(row)
		}
	}
	// A later timezone/window/measure edit cannot reuse a release elsewhere.
	changed := m
	changed.Location = "America/New_York"
	b, err := mandate.ReadLimitBalance(ctx, s, tenant, changed, m.Aggregates[0], base.Add(2*time.Second), "")
	if err != nil || b.Released != 0 {
		t.Fatalf("credit crossed timebase %+v %v", b, err)
	}
}

func TestMixedEffectReleaseIdempotentAndOperationStaysSpent(t *testing.T) {
	testMixedReleases(t, mandate.NewMemoryStore(), tenantID)
}

func TestPostgres_MixedEffectReleaseIdempotentAndOperationStaysSpent(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "effects-mixed")
	testMixedReleases(t, mandate.NewPostgresStore(p), "effects-mixed")
}

func testEffectReleaseWindowBoundaries(t *testing.T, s mandate.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC) // Paris midnight before the 23-hour day.
	m := homeCard("dst-effects", 50000, 500000)
	m.TenantID, m.Location, m.ValidFrom, m.ValidUntil = tenant, "Europe/Paris", time.Time{}, time.Time{}
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	auth := mandate.NewAuthorizer(s, nil, at(now))
	if d, err := auth.Authorize(ctx, tenant, "dst-operation", refund(40000)); err != nil || !d.Allowed() {
		t.Fatal(d, err)
	}
	draws, err := s.ProposalDraws(ctx, tenant, "dst-operation")
	if err != nil || len(draws) != 1 || len(draws[0].ChargedLimits) != 1 {
		t.Fatal(draws, err)
	}
	term := draws[0].ChargedLimits[0]
	if term.Location != "Europe/Paris" || term.WindowEnd.Sub(term.WindowStart) != 23*time.Hour {
		t.Fatalf("original DST boundary: %+v", term)
	}
	if _, err := s.(mandate.EffectReleaseStore).ReleaseEffects(ctx, effectEvidence(tenant, "dst-operation", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		at             time.Time
		gross, release int64
	}{
		{"inside", now.Add(2 * time.Hour), 40000, 40000},
		{"next_midnight", term.WindowEnd, 0, 0},
		{"next_day", term.WindowEnd.Add(time.Hour), 0, 0},
	} {
		b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, m.Aggregates[0], tc.at, "")
		if err != nil || b.Gross != tc.gross || b.Released != tc.release {
			t.Fatalf("%s: %+v %v", tc.name, b, err)
		}
	}
	changed := m.Aggregates[0]
	changed.Window = mandate.WindowRollingDay
	b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, changed, now.Add(2*time.Hour), "")
	if err != nil || b.Gross != 40000 || b.Released != 0 {
		t.Fatalf("credit crossed edited window: %+v %v", b, err)
	}
}

func TestEffectReleaseOriginalDSTAndEditedWindow(t *testing.T) {
	testEffectReleaseWindowBoundaries(t, mandate.NewMemoryStore(), tenantID)
}

func TestPostgres_EffectReleaseOriginalDSTAndEditedWindow(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "dst-release")
	testEffectReleaseWindowBoundaries(t, mandate.NewPostgresStore(p), "dst-release")
}

func TestLegacyDrawCannotBeCompensated(t *testing.T) {
	s := mandate.NewMemoryStore()
	ctx := context.Background()
	// Stage an immutable version-zero historical draw, without today's writer.
	d := mandate.Draw{TenantID: tenantID, MandateID: "a", ProposalID: "legacy-effect", QuantityKey: amountQ.Key(), Amount: 40000, Subject: refund(1).Subject, Action: refund(1).Action, DrawnAt: base}
	if err := s.Record(ctx, d); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ReleaseEffects(ctx, effectEvidence(tenantID, d.ProposalID, base.Add(time.Second)))
	if err != nil || len(rows) != 0 {
		t.Fatal("legacy compensation", rows, err)
	}
	m := homeCard("a", 50000, 500000)
	b, err := mandate.ReadLimitBalance(ctx, s, tenantID, m, m.Aggregates[0], base.Add(time.Minute), "")
	if err != nil || b.Net != 40000 || b.Released != 0 {
		t.Fatal("legacy balance rewritten", b, err)
	}
}

func testReleasedEffectsRestoreRoomWithoutReturningAttempts(t *testing.T, s mandate.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	m := homeCard("restored-room", 50000, 100)
	m.TenantID = tenant
	m.Aggregates = append(m.Aggregates, mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 2, Window: mandate.WindowCalendarDay})
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	auth := mandate.NewAuthorizer(s, nil, at(base))
	if d, err := auth.Authorize(ctx, tenant, "first-effect", refund(100)); err != nil || !d.Allowed() {
		t.Fatal(d, err)
	}
	if d, err := auth.Authorize(ctx, tenant, "still-charged", refund(100)); err != nil || d.Allowed() {
		t.Fatal("unsettled effect freed room", d, err)
	}
	if _, err := s.(mandate.EffectReleaseStore).ReleaseEffects(ctx, effectEvidence(tenant, "first-effect", base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	auth = mandate.NewAuthorizer(s, nil, at(base.Add(2*time.Second)))
	if d, err := auth.Authorize(ctx, tenant, "second-effect", refund(100)); err != nil || !d.Allowed() {
		t.Fatal("trusted release did not restore effect room", d, err)
	}
	if _, err := s.(mandate.EffectReleaseStore).ReleaseEffects(ctx, effectEvidence(tenant, "second-effect", base.Add(3*time.Second))); err != nil {
		t.Fatal(err)
	}
	auth = mandate.NewAuthorizer(s, nil, at(base.Add(4*time.Second)))
	if d, err := auth.Authorize(ctx, tenant, "third-effect", refund(100)); err != nil || d.Allowed() {
		t.Fatal("attempts returned after refusal", d, err)
	}
	b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, m.Aggregates[0], base.Add(4*time.Second), "")
	if err != nil || b.Net != 0 || b.Gross != 200 || b.Released != 200 {
		t.Fatal(b, err)
	}
}

func TestReleasedEffectsRestoreRoomWithoutReturningAttempts(t *testing.T) {
	testReleasedEffectsRestoreRoomWithoutReturningAttempts(t, mandate.NewMemoryStore(), tenantID)
}

func TestPostgres_ReleasedEffectsRestoreRoomWithoutReturningAttempts(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "released-room")
	testReleasedEffectsRestoreRoomWithoutReturningAttempts(t, mandate.NewPostgresStore(p), "released-room")
}

func TestEffectReleaseFailureAndBinding(t *testing.T) {
	r := newRig(t, homeCard("a", 50000, 500000))
	r.decide("effect", 40000)
	ctx := context.Background()
	e := effectEvidence(tenantID, "effect", base.Add(time.Second))
	e.Subject = "other"
	if _, err := r.store.ReleaseEffects(ctx, e); err == nil {
		t.Fatal("wrong subject accepted")
	}
	e = effectEvidence(tenantID, "effect", base.Add(time.Second))
	r.store.FailRecordWith(errors.New("storage outage"))
	if _, err := r.store.ReleaseEffects(ctx, e); err == nil {
		t.Fatal("write succeeded")
	}
	rs, _ := r.store.EffectReleases(ctx, tenantID, "a")
	if len(rs) != 0 {
		t.Fatal("partial compensation", rs)
	}
	r.store.FailRecordWith(nil)
	if _, err := r.store.ReleaseEffects(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := homeCard("a", 50000, 500000)
	b, err := mandate.ReadLimitBalance(ctx, r.store, tenantID, m, m.Aggregates[0], base.Add(24*time.Hour), "")
	if err != nil || b.Released != 0 || b.Net != 0 {
		t.Fatalf("credit in later window %+v %v", b, err)
	}
	if err := r.store.Revoke(ctx, tenantID, "a", base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	auth := mandate.NewAuthorizer(r.store, nil, at(base.Add(2*time.Second)))
	if d, _ := auth.Authorize(ctx, tenantID, "new", refund(100)); d.Allowed() {
		t.Fatal("release restored revoked authority", d)
	}
}

func TestExtraRoomDurationAndRetry(t *testing.T) {
	ctx := context.Background()
	for _, window := range []mandate.Window{mandate.WindowCalendarDay, mandate.WindowRollingHour, mandate.WindowRollingDay} {
		for _, duration := range []mandate.RoomDuration{"", mandate.RoomUntilDate, mandate.RoomPermanent} {
			t.Run(string(window)+"/"+string(duration), func(t *testing.T) {
				s := mandate.NewMemoryStore()
				s.SetClock(at(base))
				m := homeCard("a", 50000, 500000)
				m.Aggregates[0].Window = window
				if err := s.Issue(ctx, m, "owner"); err != nil {
					t.Fatal(err)
				}
				req := mandate.RoomRequest{ID: "grant-1", Quantity: m.Aggregates[0].Quantity.Key(), Measure: mandate.MeasureEffects, Window: window, Amount: 10000, Duration: duration}
				if duration == mandate.RoomUntilDate {
					req.Until = base.Add(2 * time.Hour)
				}
				got, err := s.GrantRoom(ctx, tenantID, "a", req, "owner", base)
				if err != nil {
					t.Fatal(err)
				}
				if got.Aggregates[0].Ceiling(got, base) != 510000 {
					t.Fatal(got.Aggregates)
				}
				later, err := s.GrantRoom(ctx, tenantID, "a", req, "owner", base.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				if len(later.Aggregates[0].ExtraRoom) != 1 {
					t.Fatal(later.Aggregates)
				}
				grant := got.Aggregates[0].ExtraRoom[0]
				if !grant.ExpiresAt.Equal(later.Aggregates[0].ExtraRoom[0].ExpiresAt) {
					t.Fatal("retry renewed room")
				}
				if duration != mandate.RoomPermanent && got.Aggregates[0].Ceiling(got, grant.ExpiresAt) != 500000 {
					t.Fatal("room did not expire")
				}
				revs, _ := s.Revisions(ctx, tenantID, "a")
				if len(revs) != 2 {
					t.Fatal("duplicate revision", revs)
				}
			})
		}
	}
}

func TestExtraRoomDSTAndValidation(t *testing.T) {
	m := homeCard("a", 50000, 500000)
	m.Location = "Europe/Paris"
	m.ValidFrom = time.Time{}
	m.ValidUntil = time.Time{}
	now := time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC)
	req := mandate.RoomRequest{ID: "dst", Quantity: m.Aggregates[0].Quantity.Key(), Measure: mandate.MeasureEffects, Window: mandate.WindowCalendarDay, Amount: 1}
	m, err := mandate.RoomFor(m, req, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	end := m.Aggregates[0].ExtraRoom[0].ExpiresAt
	if end.Sub(now) != 23*time.Hour {
		t.Fatalf("DST duration %s", end.Sub(now))
	}
	req.ID = "bad"
	req.Duration = "forever-ish"
	if _, err := mandate.RoomFor(m, req, "owner", now); err == nil {
		t.Fatal("bad duration accepted")
	}
	req.Duration = mandate.RoomUntilDate
	req.Until = now
	if _, err := mandate.RoomFor(m, req, "owner", now); err == nil {
		t.Fatal("expired room accepted")
	}
}

func TestPostgres_ExtraRoomFixedExpiryAndConcurrentConfirmation(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "room-owner")
	s := mandate.NewPostgresStore(p)
	ctx := context.Background()
	m := homeCard("room", 50000, 500000)
	m.TenantID = "room-owner"
	m.Aggregates[0].Window = mandate.WindowRollingHour
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	end := base.Add(time.Hour)
	req := mandate.RoomRequest{ID: "confirmed-room", Quantity: amountQ.Key(), Window: mandate.WindowRollingHour, Measure: mandate.MeasureEffects, Amount: 1000, WindowEnd: end}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.GrantRoom(ctx, "room-owner", "room", req, "owner", base.Add(time.Minute)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ms, err := s.ForSubject(ctx, "room-owner", m.Subject)
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	a := ms[0].Aggregates[0]
	if a.Limit != 500000 || len(a.ExtraRoom) != 1 || !a.ExtraRoom[0].ExpiresAt.Equal(end) || a.Ceiling(ms[0], end) != 500000 {
		t.Fatal(a)
	}
	revs, err := s.Revisions(ctx, "room-owner", "room")
	if err != nil || len(revs) != 2 {
		t.Fatal(revs, err)
	}
	// Removing the grant in a deliberate edit cannot renew its revision id.
	changed := ms[0]
	changed.Aggregates[0].ExtraRoom = nil
	changed.Aggregates[0].Measure = mandate.MeasureAttempts
	if err := s.Issue(ctx, changed, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantRoom(ctx, "room-owner", "room", req, "owner", base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.ForSubject(ctx, "room-owner", m.Subject)
	if ms[0].Aggregates[0].Ceiling(ms[0], base.Add(2*time.Minute)) != 500000 {
		t.Fatal("replayed revision restored authority", ms)
	}
}

func TestPostgres_EffectReleaseBatchRollsBackAndDoesNotRewriteDraws(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "atomic-effects")
	s := mandate.NewPostgresStore(p)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		m := homeCard(id, 50000, 500000)
		m.TenantID = "atomic-effects"
		if err := s.Issue(ctx, m, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	if d, err := a.Authorize(ctx, "atomic-effects", "batch-op", refund(40000)); err != nil || !d.Allowed() {
		t.Fatal(d, err)
	}
	var before string
	if err := p.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(d) ORDER BY mandate_id)::text FROM cloud_meta.mandate_draws d WHERE tenant_id='atomic-effects'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `CREATE FUNCTION cloud_meta.fail_second_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.mandate_id='b' THEN RAISE EXCEPTION 'fixture write failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER fixture_fail_release BEFORE INSERT ON cloud_meta.mandate_effect_releases FOR EACH ROW EXECUTE FUNCTION cloud_meta.fail_second_release()`); err != nil {
		t.Fatal(err)
	}
	e := effectEvidence("atomic-effects", "batch-op", base.Add(time.Second))
	if _, err := s.ReleaseEffects(ctx, e); err == nil {
		t.Fatal("failed batch succeeded")
	}
	var count int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.mandate_effect_releases WHERE tenant_id='atomic-effects'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial release", count, err)
	}
	if _, err := p.Exec(ctx, `DROP TRIGGER fixture_fail_release ON cloud_meta.mandate_effect_releases; DROP FUNCTION cloud_meta.fail_second_release()`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if rows, err := s.ReleaseEffects(ctx, e); err != nil || len(rows) != 2 {
			t.Fatal(rows, err)
		}
	}
	var after string
	if err := p.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(d) ORDER BY mandate_id)::text FROM cloud_meta.mandate_draws d WHERE tenant_id='atomic-effects'`).Scan(&after); err != nil || before != after {
		t.Fatal("original draws rewritten", err)
	}
}
