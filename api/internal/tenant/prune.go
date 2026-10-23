package tenant

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Prune keeps a test account small (the owner, 22 Oct 2026): what it
// finished with before a cutoff is deleted, and the account goes on as it
// was. Its people and roles, its settings, its live keys, live Cards and
// what they have drawn, and anything still open stay. Only for accounts an
// operator names (KIFF_PRUNE_TENANTS, kiff-tenant prune): a customer's
// Record is never pruned. Pruning also forgets what makes an old
// operation old: its permit binding, its first decision and its gateway
// call. A retry of an operation id from before the cutoff is then decided
// as new, may bind other arguments, and no longer counts as asked before a
// halt. That is fine for test accounts, which never replay work that old,
// and is why a customer's account must never be named.
//
// An operation's history (its events, decisions, Record entries and
// approvals, in the account's schema) goes only once none of it is newer
// than the cutoff and none of it waits for an answer, so an entity's state
// is never rebuilt from part of its history.
//
// Every cloud_meta table appears in exactly one of cloudMetaPruned,
// cloudMetaKeptOnPrune or cloudMetaNotPerTenant;
// TestCloudMetaTablesAreClassifiedForPrune fails on a table in none.

// pruneStep deletes from one table. Its statement takes $1, the tenant,
// and $2, the cutoff.
type pruneStep struct {
	table string
	sql   string
}

// endedCards are the account's Cards revoked, expired or archived before
// the cutoff.
const endedCards = `SELECT id FROM cloud_meta.mandates WHERE tenant_id = $1
	AND (revoked_at < $2 OR valid_until < $2 OR archived_at < $2)`

// cloudMetaPruned run in order, children before parents.
var cloudMetaPruned = []pruneStep{
	// Cards that ended, and what only they hold. Their draws, holders,
	// revisions and warnings go with them (ON DELETE CASCADE).
	{"approver_grants", `DELETE FROM cloud_meta.approver_grants WHERE tenant_id = $1
		AND (revoked_at < $2 OR card_id IN (` + endedCards + `))`},
	{"mandate_effect_releases", `DELETE FROM cloud_meta.mandate_effect_releases WHERE tenant_id = $1
		AND mandate_id IN (` + endedCards + `)`},
	{"mandate_exception_entries", `DELETE FROM cloud_meta.mandate_exception_entries WHERE tenant_id = $1
		AND mandate_id IN (` + endedCards + `)`},
	{"trial_draws", `DELETE FROM cloud_meta.trial_draws WHERE tenant_id = $1
		AND mandate_id IN (` + endedCards + `)`},
	{"mandates", `DELETE FROM cloud_meta.mandates WHERE tenant_id = $1
		AND id IN (` + endedCards + `)`},
	// Holds that ended and count toward no live Card.
	{"mandate_exceptions", `DELETE FROM cloud_meta.mandate_exceptions e WHERE e.tenant_id = $1
		AND e.created_at < $2 AND e.status NOT IN ('held', 'approved')
		AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_exception_entries x
			WHERE x.tenant_id = e.tenant_id AND x.exception_id = e.id)`},
	{"hold_answers", `DELETE FROM cloud_meta.hold_answers a WHERE a.tenant_id = $1 AND a.at < $2
		AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_exceptions e
			WHERE e.tenant_id = a.tenant_id AND e.id = a.hold_id)`},
	{"mandate_proposal_claims", `DELETE FROM cloud_meta.mandate_proposal_claims c WHERE c.tenant_id = $1
		AND c.claimed_at < $2
		AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_draws d
			WHERE d.tenant_id = c.tenant_id AND d.proposal_id = c.proposal_id)`},
	// Warnings sent, resolved or done with.
	{"mandate_warning_deliveries", `DELETE FROM cloud_meta.mandate_warning_deliveries WHERE tenant_id = $1
		AND (delivered_at < $2 OR cancelled_at < $2)`},
	{"mandate_warnings", `DELETE FROM cloud_meta.mandate_warnings WHERE tenant_id = $1 AND resolved_at < $2`},
	{"mandate_warning_work", `DELETE FROM cloud_meta.mandate_warning_work WHERE tenant_id = $1 AND completed_at < $2`},
	// Calls on the meters. A test account is never billed.
	{"billable_calls", `DELETE FROM cloud_meta.billable_calls WHERE tenant_id = $1 AND at < $2`},
	// Records.
	{"receipts", `DELETE FROM cloud_meta.receipts WHERE tenant_id = $1 AND created_at < $2`},
	{"permit_bindings", `DELETE FROM cloud_meta.permit_bindings WHERE tenant_id = $1 AND created_at < $2`},
	{"alert_deliveries", `DELETE FROM cloud_meta.alert_deliveries WHERE tenant_id = $1 AND attempted_at < $2`},
	{"alert_retries", `DELETE FROM cloud_meta.alert_retries WHERE tenant_id = $1 AND created_at < $2`},
	{"guard_observations", `DELETE FROM cloud_meta.guard_observations WHERE tenant_id = $1 AND observed_at < $2`},
	{"operator_alerts", `DELETE FROM cloud_meta.operator_alerts WHERE tenant_id = $1 AND last_seen_at < $2`},
	{"halt_events", `DELETE FROM cloud_meta.halt_events WHERE tenant_id = $1 AND at < $2`},
	// Credentials, sessions and drafts that ended.
	{"developer_sessions", `DELETE FROM cloud_meta.developer_sessions WHERE tenant_id = $1
		AND (revoked_at < $2 OR expires_at < $2)`},
	{"approval_devices", `DELETE FROM cloud_meta.approval_devices d WHERE d.tenant_id = $1 AND d.revoked_at < $2
		AND NOT EXISTS (SELECT 1 FROM cloud_meta.developer_sessions s WHERE s.device_id = d.id)`},
	{"api_keys", `DELETE FROM cloud_meta.api_keys WHERE tenant_id = $1 AND revoked_at < $2`},
	{"oauth_tokens", `DELETE FROM cloud_meta.oauth_tokens WHERE tenant_id = $1 AND expires_at < $2`},
	{"oauth_codes", `DELETE FROM cloud_meta.oauth_codes WHERE tenant_id = $1 AND expires_at < $2`},
	{"oauth_grants", `DELETE FROM cloud_meta.oauth_grants WHERE tenant_id = $1 AND ended_at < $2`},
	{"oauth_connect_links", `DELETE FROM cloud_meta.oauth_connect_links WHERE tenant_id = $1 AND revoked_at < $2`},
	{"oauth_device_codes", `DELETE FROM cloud_meta.oauth_device_codes WHERE tenant_id = $1 AND expires_at < $2`},
	{"agent_drafts", `DELETE FROM cloud_meta.agent_drafts WHERE tenant_id = $1 AND created_at < $2
		AND (status IN ('discarded', 'failed') OR (status = 'draft' AND expires_at < $2))`},
	{"tenant_invites", `DELETE FROM cloud_meta.tenant_invites WHERE tenant_id = $1
		AND (accepted_at < $2 OR revoked_at < $2 OR expires_at < $2)`},
	{"agents", `DELETE FROM cloud_meta.agents WHERE tenant_id = $1 AND archived_at < $2`},
}

// cloudMetaKeptOnPrune stay, with the reason.
var cloudMetaKeptOnPrune = map[string]string{
	"tenants":                    "the account itself",
	"tenant_memberships":         "the account's people and roles",
	"subject_tenants":            "who signs in to the account",
	"tenant_governance":          "the account's policy",
	"tenant_domains":             "settings",
	"tenant_domain_drafts":       "settings",
	"tenant_controls":            "settings",
	"permit_targets":             "settings: how each tool's permits bind",
	"alert_targets":              "settings: where alerts go",
	"guard_connections":          "the guards connected; their observations are pruned",
	"guard_decision_cursors":     "how far each guard has reconciled",
	"halts":                      "each agent's halt, on or off; a resumed one's time refuses permits from before it",
	"mandate_draws":              "what a live Card has drawn, its accounting; goes with its Card",
	"mandate_holders":            "goes with its Card",
	"mandate_revisions":          "goes with its Card",
	"mandate_warning_settings":   "goes with its Card",
	"mandate_warning_episodes":   "a live Card's warning state; goes with its Card",
	"support_tickets":            "support history",
	"support_ticket_messages":    "support history",
	"billing_customers":          "billing",
	"billing_usage_events":       "billing",
	"tenant_usage":               "billing",
	"operator_audit":             "security: what operators did",
	"membership_audit":           "security: who changed who could do what",
	"user_wallets":               "a person's, not the account's",
	"wallet_nonces":              "a person's, not the account's; expire on their own",
	"billing_processed_events":   "no account's data",
	"mandate_accounting_cutover": "no account's data",
	"oauth_clients":              "no account's data",
	"oauth_requests":             "no account's data; expire in 10 minutes",
	"signed_requests":            "no account's data; pruned after use",
}

// gatewayPruned are the gateway's rows the account finished with: calls
// settled before the cutoff, and their dedup slots. Its tools and clients
// stay.
var gatewayPruned = []pruneStep{
	{"invocations", `DELETE FROM gateway.invocations WHERE tenant_id = $1 AND updated_at < $2
		AND state NOT IN ('deciding', 'forwarding', 'held')`},
	{"dedup_slots", `DELETE FROM gateway.dedup_slots WHERE tenant_id = $1 AND opened_at < $2`},
}

// schemaPruned run in the account's own schema, with $1 the cutoff.
// prune_active, built first, holds every entity with anything newer than
// the cutoff or waiting for an answer; nothing of those is deleted.
var schemaPruned = []pruneStep{
	{"kiff_approvals", `DELETE FROM kiff_approvals WHERE inserted_at < $1
		AND entity_id NOT IN (SELECT entity_id FROM pg_temp.prune_active)`},
	{"kiff_decisions", `DELETE FROM kiff_decisions WHERE inserted_at < $1
		AND entity_id NOT IN (SELECT entity_id FROM pg_temp.prune_active)`},
	{"kiff_audit", `DELETE FROM kiff_audit WHERE inserted_at < $1
		AND entity_id NOT IN (SELECT entity_id FROM pg_temp.prune_active)`},
	{"kiff_events", `DELETE FROM kiff_events WHERE inserted_at < $1
		AND entity_id NOT IN (SELECT entity_id FROM pg_temp.prune_active)`},
	{"cloud_attention", `DELETE FROM cloud_attention WHERE status = 'resolved' AND updated_at < $1`},
	{"cloud_attention_events", `DELETE FROM cloud_attention_events e WHERE e.created_at < $1
		AND NOT EXISTS (SELECT 1 FROM cloud_attention_jobs j WHERE j.event_id = e.id AND j.completed_at IS NULL)`},
}

const pruneActiveSQL = `CREATE TEMP TABLE prune_active ON COMMIT DROP AS
	SELECT entity_id FROM kiff_events WHERE inserted_at >= $1
	UNION SELECT entity_id FROM kiff_decisions WHERE inserted_at >= $1
	UNION SELECT entity_id FROM kiff_audit WHERE inserted_at >= $1
	UNION SELECT entity_id FROM kiff_approvals WHERE inserted_at >= $1 OR status = 'pending'`

// MinPruneAge is the youngest cutoff Prune accepts: a day, so a slip in
// configuration can't empty an account's Record of today's work.
const MinPruneAge = 24 * time.Hour

// Pruned is how many rows each table lost, keyed schema.table.
type Pruned map[string]int64

// Total is every row pruned.
func (p Pruned) Total() int64 {
	var n int64
	for _, v := range p {
		n += v
	}
	return n
}

// Prune deletes what tenantID finished with before now-age, in one
// transaction. dry counts the same rows and keeps them. Tables a database
// doesn't have are skipped.
func (p *Provisioner) Prune(ctx context.Context, tenantID string, age time.Duration, now time.Time, dry bool) (Pruned, error) {
	if age < MinPruneAge {
		return nil, fmt.Errorf("prune: %s is younger than %s", age, MinPruneAge)
	}
	t, err := p.store.Get(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	if t.Status != StatusActive {
		return nil, fmt.Errorf("prune: tenant %s is %s, not active", tenantID, t.Status)
	}
	schema := SchemaNameFor(tenantID)
	if !validSchemaName(schema) {
		return nil, fmt.Errorf("%w: derived schema name failed validation: %s", ErrInvalidTenant, schema)
	}
	cutoff := now.Add(-age)

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	defer tx.Rollback(ctx)

	out := Pruned{}
	exists := func(schema, table string) (bool, error) {
		var ok bool
		err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{schema, table}.Sanitize()).Scan(&ok)
		return ok, err
	}
	run := func(schema string, steps []pruneStep, args ...any) error {
		for _, s := range steps {
			ok, err := exists(schema, s.table)
			if err != nil {
				return fmt.Errorf("prune %s.%s: %w", schema, s.table, err)
			}
			if !ok {
				continue
			}
			tag, err := tx.Exec(ctx, s.sql, args...)
			if err != nil {
				return fmt.Errorf("prune %s.%s: %w", schema, s.table, err)
			}
			if n := tag.RowsAffected(); n > 0 {
				out[schema+"."+s.table] += n
			}
		}
		return nil
	}
	if err := run(MetaSchema, cloudMetaPruned, tenantID, cutoff); err != nil {
		return nil, err
	}
	if err := run(gatewaySchema, gatewayPruned, tenantID, cutoff); err != nil {
		return nil, err
	}

	// The account's own schema: its framework tables must all be there,
	// or an entity's activity could be missed.
	for _, table := range []string{"kiff_events", "kiff_decisions", "kiff_audit", "kiff_approvals"} {
		ok, err := exists(schema, table)
		if err != nil {
			return nil, fmt.Errorf("prune %s.%s: %w", schema, table, err)
		}
		if !ok {
			return nil, fmt.Errorf("prune: %s has no %s", schema, table)
		}
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	if _, err := tx.Exec(ctx, pruneActiveSQL, cutoff); err != nil {
		return nil, fmt.Errorf("prune: active entities: %w", err)
	}
	if err := run(schema, schemaPruned, cutoff); err != nil {
		return nil, err
	}

	if dry {
		return out, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	return out, nil
}
