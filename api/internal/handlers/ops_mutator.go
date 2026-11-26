package handlers

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// OpsMutator performs an operator state change and its audit record
// together (the operator-console design atomic-audit guarantee). The Postgres
// implementation runs both in one transaction, so a state change can
// never commit without its audit row; the memory implementation
// records audit first and applies state only if the audit write
// succeeds, giving tests the same "no silent mutation" guarantee.
//
// Because a failed mutation leaves state UNCHANGED, a retry after a
// transient audit failure is not a no-op — it re-applies and writes
// the audit row, closing the gap the review flagged.
type OpsMutator interface {
	// SetTenantStatus moves the tenant from status from to status and
	// writes e in the same atomic unit. It changes nothing, and answers
	// tenant.ErrStatusChanged, when the tenant is no longer in from.
	SetTenantStatus(ctx context.Context, tenantID string, from, status tenant.Status, prov *time.Time, e operatoraudit.Entry) error
	// SetTenantPlan sets the tenant's plan metadata key and writes e.
	SetTenantPlan(ctx context.Context, tenantID, plan string, e operatoraudit.Entry) error
	// SetTenantMetadata sets each of set's keys on the tenant ("" clears
	// one) and writes e, in the same atomic unit.
	SetTenantMetadata(ctx context.Context, tenantID string, set map[string]any, e operatoraudit.Entry) error
	// ResetStoreBilling is Reset store billing's write: set's keys, as
	// SetTenantMetadata, and the account's App Store and Google Play packs
	// with credits left set aside at at (packs.SetAsideStorePacksQ), with e,
	// which also says what was set aside (store_packs_set_aside,
	// store_pack_credits_set_aside), in the same atomic unit. It takes every
	// store pack on the account, so it's only for an account the reset's
	// guard allows (a review or test account): see packs.SetAsideStorePacksQ.
	ResetStoreBilling(ctx context.Context, tenantID string, set map[string]any, at time.Time, e operatoraudit.Entry) (packs.StorePacksSetAside, error)
	// SetTenantLabel sets the operators' label on the tenant ("" for
	// none) and writes e.
	SetTenantLabel(ctx context.Context, tenantID, label string, e operatoraudit.Entry) error
	// AlertStatus transitions an alert and writes e (augmented with
	// the alert's tenant + kind) in the same atomic unit; returns the
	// transitioned alert.
	AlertStatus(ctx context.Context, alertID, status, by, note string, e operatoraudit.Entry) (operatoralerts.Alert, error)
}

// ── memory mutator (tests / no-Postgres) ──────────────────────────

type memOpsMutator struct {
	tenants tenant.Store
	audit   operatoraudit.Store
	alerts  operatoralerts.Store
	// packs is the in-memory pack ledger, when the handler has one: the
	// packs Reset store billing sets aside. nil sets none aside.
	packs *packs.Memory
}

func newMemOpsMutator(t tenant.Store, a operatoraudit.Store, al operatoralerts.Store) *memOpsMutator {
	return &memOpsMutator{tenants: t, audit: a, alerts: al}
}

func (m *memOpsMutator) SetTenantStatus(ctx context.Context, tenantID string, from, status tenant.Status, prov *time.Time, e operatoraudit.Entry) error {
	if t, err := m.tenants.Get(ctx, tenantID); err != nil {
		return err
	} else if t.Status != from {
		return tenant.ErrStatusChanged
	}
	// Audit-first: if the audit write fails, state is not touched.
	if err := m.audit.Record(ctx, e); err != nil {
		return err
	}
	return m.tenants.SetStatus(ctx, tenantID, status, prov)
}

func (m *memOpsMutator) SetTenantPlan(ctx context.Context, tenantID, plan string, e operatoraudit.Entry) error {
	if err := m.audit.Record(ctx, e); err != nil {
		return err
	}
	return m.tenants.SetMetadata(ctx, tenantID, tenant.MetadataKeyPlan, plan)
}

func (m *memOpsMutator) SetTenantMetadata(ctx context.Context, tenantID string, set map[string]any, e operatoraudit.Entry) error {
	if err := m.audit.Record(ctx, e); err != nil {
		return err
	}
	for _, k := range sortedKeys(set) {
		if err := m.tenants.SetMetadata(ctx, tenantID, k, set[k]); err != nil {
			return err
		}
	}
	return nil
}

func (m *memOpsMutator) ResetStoreBilling(ctx context.Context, tenantID string, set map[string]any, at time.Time, e operatoraudit.Entry) (packs.StorePacksSetAside, error) {
	var aside packs.StorePacksSetAside
	if m.packs != nil {
		aside = m.packs.StorePacks(tenantID)
	}
	if err := m.SetTenantMetadata(ctx, tenantID, set, withStorePacks(e, aside)); err != nil {
		return packs.StorePacksSetAside{}, err
	}
	if m.packs != nil {
		aside = m.packs.SetAsideStorePacks(tenantID, at)
	}
	return aside, nil
}

// withStorePacks is e, its metadata also saying what Reset store billing set
// aside.
func withStorePacks(e operatoraudit.Entry, aside packs.StorePacksSetAside) operatoraudit.Entry {
	meta := make(map[string]any, len(e.Metadata)+2)
	for k, v := range e.Metadata {
		meta[k] = v
	}
	meta["store_packs_set_aside"], meta["store_pack_credits_set_aside"] = aside.Packs, aside.Credits
	e.Metadata = meta
	return e
}

func (m *memOpsMutator) SetTenantLabel(ctx context.Context, tenantID, label string, e operatoraudit.Entry) error {
	if err := m.audit.Record(ctx, e); err != nil {
		return err
	}
	return m.tenants.SetMetadata(ctx, tenantID, opsLabelKey, label)
}

func (m *memOpsMutator) AlertStatus(ctx context.Context, alertID, status, by, note string, e operatoraudit.Entry) (operatoralerts.Alert, error) {
	// The audit entry needs the alert's tenant, known only after the
	// transition, so transition first, then audit; the audit error is
	// surfaced (never ignored).
	a, err := m.alerts.SetStatus(ctx, alertID, status, by, note)
	if err != nil {
		return operatoralerts.Alert{}, err
	}
	e.TargetTenantID = a.TenantID
	e.Metadata = map[string]any{"alert_id": a.ID, "kind": a.Kind}
	if aerr := m.audit.Record(ctx, e); aerr != nil {
		return a, aerr
	}
	return a, nil
}

// ── Postgres mutator (production) ─────────────────────────────────

type pgOpsMutator struct {
	pool *pgxpool.Pool
}

// NewPostgresOpsMutator builds the transactional mutator over the
// cloud pool. Each method runs the state change and the audit insert
// in one transaction: both commit or both roll back.
func NewPostgresOpsMutator(pool *pgxpool.Pool) OpsMutator {
	return &pgOpsMutator{pool: pool}
}

func (m *pgOpsMutator) SetTenantStatus(ctx context.Context, tenantID string, from, status tenant.Status, prov *time.Time, e operatoraudit.Entry) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := tenant.SetStatusFromQ(ctx, tx, tenantID, from, status, prov); err != nil {
		return err
	}
	if err := operatoraudit.RecordQ(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *pgOpsMutator) SetTenantPlan(ctx context.Context, tenantID, plan string, e operatoraudit.Entry) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := tenant.SetMetadataKeyQ(ctx, tx, tenantID, tenant.MetadataKeyPlan, plan); err != nil {
		return err
	}
	if err := operatoraudit.RecordQ(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *pgOpsMutator) SetTenantMetadata(ctx context.Context, tenantID string, set map[string]any, e operatoraudit.Entry) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, k := range sortedKeys(set) {
		if err := tenant.SetMetadataKeyQ(ctx, tx, tenantID, k, set[k]); err != nil {
			return err
		}
	}
	if err := operatoraudit.RecordQ(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *pgOpsMutator) ResetStoreBilling(ctx context.Context, tenantID string, set map[string]any, at time.Time, e operatoraudit.Entry) (packs.StorePacksSetAside, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return packs.StorePacksSetAside{}, err
	}
	defer tx.Rollback(ctx)
	for _, k := range sortedKeys(set) {
		if err := tenant.SetMetadataKeyQ(ctx, tx, tenantID, k, set[k]); err != nil {
			return packs.StorePacksSetAside{}, err
		}
	}
	aside, err := packs.SetAsideStorePacksQ(ctx, tx, tenantID, at)
	if err != nil {
		return packs.StorePacksSetAside{}, err
	}
	if err := operatoraudit.RecordQ(ctx, tx, withStorePacks(e, aside)); err != nil {
		return packs.StorePacksSetAside{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return packs.StorePacksSetAside{}, err
	}
	return aside, nil
}

// sortedKeys is set's keys in order, so writes go in the same order each time.
func sortedKeys(set map[string]any) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (m *pgOpsMutator) SetTenantLabel(ctx context.Context, tenantID, label string, e operatoraudit.Entry) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := tenant.SetMetadataKeyQ(ctx, tx, tenantID, opsLabelKey, label); err != nil {
		return err
	}
	if err := operatoraudit.RecordQ(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *pgOpsMutator) AlertStatus(ctx context.Context, alertID, status, by, note string, e operatoraudit.Entry) (operatoralerts.Alert, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return operatoralerts.Alert{}, err
	}
	defer tx.Rollback(ctx)
	a, err := operatoralerts.SetStatusQ(ctx, tx, alertID, status, by, note)
	if err != nil {
		return operatoralerts.Alert{}, err
	}
	e.TargetTenantID = a.TenantID
	e.Metadata = map[string]any{"alert_id": a.ID, "kind": a.Kind}
	if err := operatoraudit.RecordQ(ctx, tx, e); err != nil {
		return operatoralerts.Alert{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return operatoralerts.Alert{}, err
	}
	return a, nil
}
