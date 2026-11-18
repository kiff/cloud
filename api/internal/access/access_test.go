package access_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/pgschema"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

func member(subject string, role membership.Role) *membership.Member {
	return &membership.Member{TenantID: "t1", Subject: subject, Role: role, Status: membership.StatusActive}
}

// Use comes from a grant to the person or a group they're in;
// oversee for a manager of a group the agent is granted, or an admin;
// manage from cards.manage. Being on the account gives nothing else.
func TestDecide(t *testing.T) {
	support := []access.GroupMember{{Group: "support", Subject: "maria", Role: access.Manager}}
	grants := []access.Grant{{Agent: "refunds", Group: "support"}, {Agent: "refunds", Subject: "luis"}}
	for _, c := range []struct {
		name   string
		m      *membership.Member
		groups []access.GroupMember
		want   access.Answer
	}{
		{"a viewer in no group, not granted", member("paul", membership.RoleViewer), nil, access.Answer{Groups: []string{}}},
		{"granted by name", member("luis", membership.RoleViewer), nil, access.Answer{Use: true, Groups: []string{}}},
		{"a member of a granted group", member("ana", membership.RoleApprover),
			[]access.GroupMember{{Group: "support", Subject: "ana", Role: access.Member}}, access.Answer{Use: true, Groups: []string{"support"}}},
		{"its manager oversees", member("maria", membership.RoleViewer), support, access.Answer{Use: true, Oversee: true, Groups: []string{"support"}}},
		{"an editor manages, without use", member("ed", membership.RoleEditor), nil, access.Answer{Manage: true, Groups: []string{}}},
		{"an admin oversees and manages every agent", member("ada", membership.RoleAdmin), nil, access.Answer{Oversee: true, Manage: true, Groups: []string{}}},
		{"not on the account", nil, support, access.Answer{Groups: []string{}}},
	} {
		got := access.Decide(c.m, c.groups, grants)
		if got.Use != c.want.Use || got.Oversee != c.want.Oversee || got.Manage != c.want.Manage || !slices.Equal(got.Groups, c.want.Groups) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	suspended := member("ana", membership.RoleAdmin)
	suspended.Status = membership.StatusSuspended
	if got := access.Decide(suspended, nil, grants); got.Use || got.Oversee || got.Manage {
		t.Errorf("a suspended admin: %+v", got)
	}
}

// storeContract runs the same checks on every Store.
func storeContract(t *testing.T, s access.Store, tenantID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateGroup(ctx, access.Group{TenantID: tenantID, ID: "g1", Name: "Support", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, access.Group{TenantID: tenantID, ID: "g2", Name: "support", CreatedAt: now}); !errors.Is(err, access.ErrExists) {
		t.Errorf("a name in use, any case: %v", err)
	}
	if err := s.CreateGroup(ctx, access.Group{TenantID: tenantID, ID: "g2", Name: "Finance", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGroup(ctx, tenantID, "g2", "SUPPORT"); !errors.Is(err, access.ErrExists) {
		t.Errorf("rename onto a name in use: %v", err)
	}
	if err := s.SetMember(ctx, access.GroupMember{TenantID: tenantID, Group: "nope", Subject: "ana", Role: access.Member, AddedAt: now}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a member of no group: %v", err)
	}
	for _, m := range []access.GroupMember{
		{TenantID: tenantID, Group: "g1", Subject: "maria", Role: access.Member, AddedAt: now},
		{TenantID: tenantID, Group: "g1", Subject: "maria", Role: access.Manager, AddedAt: now},
		{TenantID: tenantID, Group: "g1", Subject: "ana", Role: access.Member, AddedAt: now},
		{TenantID: tenantID, Group: "g2", Subject: "ana", Role: access.Member, AddedAt: now},
	} {
		if err := s.SetMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := s.Members(ctx, tenantID, "g1")
	if len(ms) != 2 || ms[1].Subject != "maria" || ms[1].Role != access.Manager {
		t.Errorf("members: %+v", ms)
	}
	if mine, _ := s.MembershipsOf(ctx, tenantID, "ana"); len(mine) != 2 {
		t.Errorf("ana's groups: %+v", mine)
	}
	if err := s.AddGrant(ctx, access.Grant{TenantID: tenantID, Agent: "refunds", Group: "nope", CreatedAt: now}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a grant to no group: %v", err)
	}
	for _, g := range []access.Grant{
		{TenantID: tenantID, Agent: "refunds", Group: "g1", CreatedAt: now},
		{TenantID: tenantID, Agent: "refunds", Group: "g1", CreatedAt: now}, // again: no change
		{TenantID: tenantID, Agent: "refunds", Subject: "luis", CreatedAt: now},
		{TenantID: tenantID, Agent: "invoices", Group: "g2", CreatedAt: now},
	} {
		if err := s.AddGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if gs, _ := s.Grants(ctx, tenantID, "refunds"); len(gs) != 2 {
		t.Errorf("refunds' grants: %+v", gs)
	}
	if gs, _ := s.Grants(ctx, tenantID, ""); len(gs) != 3 {
		t.Errorf("all grants: %+v", gs)
	}
	if err := s.RemoveGrant(ctx, access.Grant{TenantID: tenantID, Agent: "refunds", Subject: "luis"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveGrant(ctx, access.Grant{TenantID: tenantID, Agent: "refunds", Subject: "luis"}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("removed twice: %v", err)
	}
	// Deleting a group takes its members and grants with it.
	if err := s.DeleteGroup(ctx, tenantID, "g2"); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.Grants(ctx, tenantID, "invoices"); len(gs) != 0 {
		t.Errorf("a deleted group's grants: %+v", gs)
	}
	if mine, _ := s.MembershipsOf(ctx, tenantID, "ana"); len(mine) != 1 {
		t.Errorf("a deleted group's members: %+v", mine)
	}
	if err := s.RemoveMember(ctx, tenantID, "g1", "ana"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, tenantID, "g1", "ana"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("removed twice: %v", err)
	}
	// Another account's group is not found.
	if _, err := s.Group(ctx, "other-"+tenantID, "g1"); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("another account's group: %v", err)
	}
	// A new agent goes to its creator, only while no one has it.
	if err := access.GrantCreator(ctx, s, tenantID, "new-taf", "ana", now); err != nil {
		t.Fatal(err)
	}
	if err := access.GrantCreator(ctx, s, tenantID, "refunds", "ana", now); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.Grants(ctx, tenantID, "new-taf"); len(gs) != 1 || gs[0].Subject != "ana" {
		t.Errorf("a new taf's grant: %+v", gs)
	}
	if gs, _ := s.Grants(ctx, tenantID, "refunds"); len(gs) != 1 || gs[0].Group != "g1" {
		t.Errorf("a shared taf kept its grants: %+v", gs)
	}
	// Someone who leaves is in no group and has no grant.
	if err := s.ForgetSubject(ctx, tenantID, "ana"); err != nil {
		t.Fatal(err)
	}
	if mine, _ := s.MembershipsOf(ctx, tenantID, "ana"); len(mine) != 0 {
		t.Errorf("a person forgotten is still in: %+v", mine)
	}
	if gs, _ := s.Grants(ctx, tenantID, "new-taf"); len(gs) != 0 {
		t.Errorf("a person forgotten keeps a grant: %+v", gs)
	}
	if ms, _ := s.Members(ctx, tenantID, "g1"); len(ms) != 1 || ms[0].Subject != "maria" {
		t.Errorf("others stay: %+v", ms)
	}
}

func TestMemoryStore(t *testing.T) {
	storeContract(t, access.NewMemoryStore(), "t1")
	readsContract(t, access.NewMemoryStore(), "t1")
}

func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
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
	if err := membership.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := agents.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := auth.ApplyAPIKeysSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func seedTenant(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if err := tenant.NewPostgresStore(pool).Create(context.Background(), tenant.Tenant{ID: id, Slug: "slug-" + id, DisplayName: id,
		Status: tenant.StatusActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStore(t *testing.T) {
	pool := requirePostgres(t)
	if err := access.ApplySchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	id := tenant.NewID()
	seedTenant(t, pool, id)
	storeContract(t, access.NewPostgresStore(pool), id)
	readsContract(t, access.NewPostgresStore(pool), id)
}

// Reads are kept as they were made and listed newest first: one owner's,
// or one agent's, never another account's.
func readsContract(t *testing.T, s access.Store, tenantID string) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	for i, r := range []access.Read{
		{ID: "rd_1", Reader: "ada", Owner: "ana", Agent: "refunds", Session: "s1", Purpose: "fix", Note: "the refund failed"},
		{ID: "rd_2", Reader: "max", Owner: "ana", Agent: "invoices", Purpose: "follow_up"},
		{ID: "rd_3", Reader: "ada", Owner: "luis", Agent: "refunds", Purpose: "concern"},
	} {
		r.TenantID, r.At = tenantID, at.Add(time.Duration(i)*time.Minute)
		if err := s.RecordRead(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(owner, agent string) []string {
		t.Helper()
		got, err := s.Reads(ctx, tenantID, owner, agent, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range got {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids("ana", ""); !slices.Equal(got, []string{"rd_2", "rd_1"}) {
		t.Errorf("Ana's reads: %v", got)
	}
	if got := ids("", "refunds"); !slices.Equal(got, []string{"rd_3", "rd_1"}) {
		t.Errorf("Refunds' reads: %v", got)
	}
	got, err := s.Reads(ctx, tenantID, "ana", "refunds", 0)
	if err != nil || len(got) != 1 || got[0].Reader != "ada" || got[0].Session != "s1" || got[0].Purpose != "fix" ||
		got[0].Note != "the refund failed" || !got[0].At.Equal(at) {
		t.Errorf("one read, as kept: %+v %v", got, err)
	}
	if other, err := s.Reads(ctx, tenantID+"-other", "", "", 0); err != nil || len(other) != 0 {
		t.Errorf("another account's reads: %v %v", other, err)
	}
}

// The first apply gives each account an "Everyone" group of
// its active people, granted every taf it has, so no one loses a taf. It
// runs once: an account, a taf or a person who comes later gets nothing
// from it.
func TestBackfillOnce(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := pgschema.Forget(ctx, pool, "access/access"); err != nil {
		t.Fatal(err)
	}
	if err := access.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// As before the first apply.
	if _, err := pool.Exec(ctx, `DELETE FROM cloud_meta.access_backfilled`); err != nil {
		t.Fatal(err)
	}
	id := tenant.NewID()
	seedTenant(t, pool, id)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO cloud_meta.tenant_memberships (id, tenant_id, subject, role, status) VALUES
		($1, $2, 'ana', 'approver', 'active'), ($3, $2, 'gone', 'viewer', 'removed')`, "m1-"+id, id, "m2-"+id)
	exec(`INSERT INTO cloud_meta.agents (tenant_id, id, kind) VALUES ($1, 'refunds', 'taf'), ($1, 'own-agent', ''), ($1, 'coach', 'taf')`, id)
	if err := auth.NewPostgresAPIKeyStore(pool).Insert(ctx, auth.APIKeyRecord{ID: "key-coach-" + id, Tenant: id, Subject: "ana",
		Roles: []string{auth.RoleSetupReader}, AgentID: "coach", KeyHash: [32]byte{byte(len(id)), 7}, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO cloud_meta.agents (tenant_id, id, kind, archived_at) VALUES ($1, 'old', 'taf', NOW())`, id)
	exec(`INSERT INTO cloud_meta.tenant_invites (id, tenant_id, email, role, invited_by, token_hash, expires_at) VALUES
		($1, $2, 'bo@x.io', 'viewer', 'ana', $3, NOW() + INTERVAL '1 day')`, "inv-"+id, id, []byte("hash-"+id))

	if err := pgschema.Forget(ctx, pool, "access/access"); err != nil {
		t.Fatal(err)
	}
	if err := access.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := access.NewPostgresStore(pool)
	g, err := s.Group(ctx, id, "everyone")
	if err != nil || !g.Everyone || g.Name != "Everyone" {
		t.Fatalf("everyone: %+v %v", g, err)
	}
	if ms, _ := s.Members(ctx, id, "everyone"); len(ms) != 1 || ms[0].Subject != "ana" || ms[0].Role != access.Member {
		t.Errorf("its people, active ones only: %+v", ms)
	}
	gs, _ := s.Grants(ctx, id, "")
	if len(gs) != 1 || gs[0].Agent != "refunds" || gs[0].Group != "everyone" {
		t.Errorf("its tafs, live ones only, not its Setup: %+v", gs)
	}

	// The invitation pending then joins its taker to Everyone, once.
	for range 2 {
		if err := s.WelcomeInvite(ctx, id, "inv-"+id, "bo", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.WelcomeInvite(ctx, id, "inv-later", "cy", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.Members(ctx, id, "everyone"); len(ms) != 2 || ms[1].Subject != "bo" {
		t.Errorf("Everyone after the invitations: %+v", ms)
	}

	// Once: a later account gets no group, and the first keeps its own.
	later := tenant.NewID()
	seedTenant(t, pool, later)
	exec(`INSERT INTO cloud_meta.agents (tenant_id, id, kind) VALUES ($1, 'later-taf', 'taf')`, id)
	if err := pgschema.Forget(ctx, pool, "access/access"); err != nil {
		t.Fatal(err)
	}
	if err := access.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if groups, _ := s.Groups(ctx, later); len(groups) != 0 {
		t.Errorf("an account made later: %+v", groups)
	}
	if gs, _ := s.Grants(ctx, id, "later-taf"); len(gs) != 0 {
		t.Errorf("a taf made later: %+v", gs)
	}
}

// Once, at boot (ClearArchivedGrants): grants left on agents archived before
// archiving removed them go, each in the trail by 'system', and the count is
// returned; a live agent keeps its own.
func TestPostgresSchemaClearsArchivedAgentsGrants(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := pgschema.Forget(ctx, pool, "access/access"); err != nil {
		t.Fatal(err)
	}
	if err := access.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	id := tenant.NewID()
	seedTenant(t, pool, id)
	now := time.Now().UTC()
	agentStore := agents.NewPostgresStore(pool)
	for _, a := range []string{"old-taf", "live-taf"} {
		if err := agentStore.Create(ctx, agents.Agent{TenantID: id, ID: a, Kind: agents.KindTaf, CreatedAt: now, CreatedBy: "ada"}); err != nil {
			t.Fatal(err)
		}
	}
	s := access.NewPostgresStore(pool)
	if err := s.CreateGroup(ctx, access.Group{TenantID: id, ID: "support", Name: "Support", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []access.Grant{
		{TenantID: id, Agent: "old-taf", Group: "support", CreatedAt: now},
		{TenantID: id, Agent: "old-taf", Subject: "ana", CreatedAt: now},
		{TenantID: id, Agent: "live-taf", Group: "support", CreatedAt: now},
	} {
		if err := s.AddGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if err := agentStore.Archive(ctx, id, "old-taf", now); err != nil {
		t.Fatal(err)
	}

	// As on the first boot with this schema: the cleanup hasn't run yet.
	if _, err := pool.Exec(ctx, `DELETE FROM cloud_meta.access_archived_grants_cleared`); err != nil {
		t.Fatal(err)
	}
	if err := pgschema.Forget(ctx, pool, "access/access"); err != nil {
		t.Fatal(err)
	}
	if err := access.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	n, err := access.ClearArchivedGrants(ctx, pool)
	if err != nil || n < 2 {
		t.Fatalf("cleared %d (at least this account's 2): %v", n, err)
	}

	if g, err := s.Grants(ctx, id, "old-taf"); err != nil || len(g) != 0 {
		t.Errorf("the archived taf's grants: %v %v", g, err)
	}
	if g, err := s.Grants(ctx, id, "live-taf"); err != nil || len(g) != 1 {
		t.Errorf("the live taf's grants: %v %v", g, err)
	}
	var removed []string
	rows, err := pool.Query(ctx, `SELECT old_value FROM cloud_meta.membership_audit
		WHERE tenant_id = $1 AND actor = 'system' AND action = 'grant.removed' AND target = 'old-taf' ORDER BY old_value`, id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		removed = append(removed, v)
	}
	if !slices.Equal(removed, []string{"group:support", "subject:ana"}) {
		t.Errorf("the trail's removals: %v", removed)
	}

	// Once: a later boot leaves a grant made since alone, even on an archived agent.
	if err := s.AddGrant(ctx, access.Grant{TenantID: id, Agent: "old-taf", Subject: "luis", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := pgschema.Forget(ctx, pool, "access/access"); err != nil {
		t.Fatal(err)
	}
	if err := access.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if n, err := access.ClearArchivedGrants(ctx, pool); err != nil || n != 0 {
		t.Errorf("a second boot cleared %d: %v", n, err)
	}
	if g, _ := s.Grants(ctx, id, "old-taf"); len(g) != 1 {
		t.Errorf("a second boot ran the cleanup again: %v", g)
	}
}
