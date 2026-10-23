package tenant

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// What a hard delete does to cloud_meta. Every cloud_meta table, whatever it is keyed by, appears
// in exactly one of the lists below; TestCloudMetaTablesAreClassified
// fails on a table in none of them, so a new table cannot skip the
// decision. eraseCloudMeta reads the same lists.

// cloudMetaErased are deleted, children before parents.
var cloudMetaErased = []string{
	"mandate_warning_deliveries",
	"mandate_warnings",
	"mandate_warning_episodes",
	"mandate_warning_work",
	"mandate_warning_settings",
	"approver_grants",
	"mandate_effect_releases",
	"mandate_draws",
	"trial_draws",
	// Which call each operation is bound to, and its permit id.
	"permit_bindings",
	"permit_targets",
	"mandate_exception_entries",
	"mandate_exceptions",
	"mandate_revisions",
	"mandate_proposal_claims",
	"mandate_holders",
	"mandates",
	"agents",
	"guard_observations",
	"guard_connections",
	"guard_decision_cursors",
	"tenant_controls",
	"tenant_domain_drafts",
	"tenant_domains",
	"alert_retries", // queued deliveries hold the payload body
	"agent_drafts",  // drafts of new agents and their Cards
	"halt_events",   // the owner's halts and resumes
	"halts",
	"alert_deliveries",
	"alert_targets",
	// Agent connections and their tokens (tokens and codes
	// first). The keys they own are api_keys rows, erased just below.
	"oauth_tokens",
	"oauth_codes",
	"oauth_grants",
	"oauth_connect_links",
	"oauth_device_codes", // pending codes carry no tenant until approved
	"api_keys",
	"developer_sessions",
	"approval_devices", // After the sessions that name them
	"support_tickets",  // its messages are deleted first, through ticket_id
	// The database copy. Anchored hashes stay on chain, unlinkable once
	// the salt is destroyed.
	"receipts",
	"operator_alerts",
	// The account's people, invitations (email addresses),
	// policy, and who answered each hold.
	"hold_answers",
	"tenant_governance",
	"tenant_invites",
	"tenant_memberships", // like subject_tenants: wallets of people left with no account follow
	"subject_tenants",    // wallets of people left with no account follow
	// Each call's agent, tool and how it counted on the meters. What
	// Stripe was told stays in billing_usage_events.
	"billable_calls",
}

// cloudMetaErasedThroughParent are keyed by their parent, not the
// tenant, and are deleted through it first: table → (column, parent).
var cloudMetaErasedThroughParent = map[string][2]string{
	"support_ticket_messages": {"ticket_id", "support_tickets"},
}

// cloudMetaErasedBySubject are keyed by a person, not the tenant. A
// person belongs to one account (subject_tenants is keyed by subject), so
// the erased account's members are left with none and these rows go.
var cloudMetaErasedBySubject = []string{
	"wallet_nonces",
	"user_wallets",
}

// cloudMetaNotPerTenant hold no tenant's or person's data.
var cloudMetaNotPerTenant = map[string]string{
	"mandate_accounting_cutover": "global accounting version and activation date; no tenant or personal data",
	"billing_processed_events":   "Stripe webhook event ids, for idempotent processing",
	"oauth_clients":              "registered OAuth clients (an app's name and redirect addresses), not an account's",
	"oauth_requests":             "pending sign-in requests, expire in 10 minutes; no account is known until consent",
	"signed_requests":            "sha256 replay ids of a phone's signed setup calls, pruned 10 minutes after use; no tenant column, nothing readable",
}

// cloudMetaRetained are kept, with the reason.
var cloudMetaRetained = map[string]string{
	"billing_customers":    "billing: the Stripe customer link, for invoices and disputes",
	"billing_usage_events": "billing: operation ids and outcomes reported to Stripe",
	"tenant_usage":         "billing: hourly counts",
	"operator_audit":       "security: what operators did",
	"membership_audit":     "security: who changed who could do what on the account",
	"tenants":              "audit: reduced to id, slug, status, dates and the deletion stamps",
}

// eraseCloudMeta applies these lists to the tenant's rows, in one
// transaction. Tables that do not exist (a database where a feature's
// schema was never applied) are skipped. Idempotent.
func (p *Provisioner) eraseCloudMeta(ctx context.Context, tenantID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("erase cloud_meta: %w", err)
	}
	defer tx.Rollback(ctx)

	exists := func(table string) (bool, error) {
		var ok bool
		err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, MetaSchema+"."+table).Scan(&ok)
		return ok, err
	}
	ident := func(table string) string { return pgx.Identifier{MetaSchema, table}.Sanitize() }

	for table, via := range cloudMetaErasedThroughParent {
		ok, err := exists(table)
		if err != nil {
			return fmt.Errorf("erase cloud_meta: %w", err)
		}
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+ident(table)+` WHERE `+pgx.Identifier{via[0]}.Sanitize()+
			` IN (SELECT id FROM `+ident(via[1])+` WHERE tenant_id = $1)`, tenantID); err != nil {
			return fmt.Errorf("erase cloud_meta: %s: %w", table, err)
		}
	}

	var subjects []string
	for _, table := range cloudMetaErased {
		ok, err := exists(table)
		if err != nil {
			return fmt.Errorf("erase cloud_meta: %w", err)
		}
		if !ok {
			continue
		}
		if table == "subject_tenants" || table == "tenant_memberships" {
			rows, err := tx.Query(ctx, `DELETE FROM `+ident(table)+` WHERE tenant_id = $1 RETURNING subject`, tenantID)
			if err != nil {
				return fmt.Errorf("erase cloud_meta: %s: %w", table, err)
			}
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					rows.Close()
					return fmt.Errorf("erase cloud_meta: %s: %w", table, err)
				}
				subjects = append(subjects, s)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("erase cloud_meta: %s: %w", table, err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+ident(table)+` WHERE tenant_id = $1`, tenantID); err != nil {
			return fmt.Errorf("erase cloud_meta: %s: %w", table, err)
		}
	}

	// A person belongs to one account (subject_tenants is keyed by
	// subject), so the account's members are left with none and their
	// wallet links go. The NOT EXISTS guard keeps a wallet whose subject
	// has meanwhile been linked to another account.
	for _, table := range cloudMetaErasedBySubject {
		ok, err := exists(table)
		if err != nil {
			return fmt.Errorf("erase cloud_meta: %w", err)
		}
		if !ok || len(subjects) == 0 {
			continue
		}
		// With memberships a person can belong to other
		// accounts too; their wallet stays while any membership does.
		stillMember := ""
		if ok, err := exists("tenant_memberships"); err != nil {
			return fmt.Errorf("erase cloud_meta: %w", err)
		} else if ok {
			stillMember = ` AND NOT EXISTS (SELECT 1 FROM ` + ident("tenant_memberships") + ` tm WHERE tm.subject = ` +
				ident(table) + `.subject AND tm.status <> 'removed')`
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+ident(table)+` WHERE subject = ANY($1)
			AND NOT EXISTS (SELECT 1 FROM `+ident("subject_tenants")+` st WHERE st.subject = `+ident(table)+`.subject)`+stillMember,
			subjects); err != nil {
			return fmt.Errorf("erase cloud_meta: %s: %w", table, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("erase cloud_meta: %w", err)
	}
	return nil
}

// reduceTenantRow clears the retained tenant row down to what audit
// needs: the display name goes, and metadata keeps only the deletion
// stamps.
func (p *Provisioner) reduceTenantRow(ctx context.Context, tenantID string) error {
	_, err := p.pool.Exec(ctx, `UPDATE `+pgx.Identifier{MetaSchema, "tenants"}.Sanitize()+`
		SET display_name = '',
		    metadata = COALESCE((SELECT jsonb_object_agg(k, v) FROM jsonb_each(COALESCE(NULLIF(metadata, 'null'::jsonb), '{}'::jsonb)) AS e(k, v)
		                         WHERE k IN ($2, $3)), '{}'::jsonb)
		WHERE id = $1`, tenantID, MetadataKeyDeletedAt, MetadataKeyGraceWindowEnds)
	if err != nil {
		return fmt.Errorf("reduce tenant row: %w", err)
	}
	return nil
}
