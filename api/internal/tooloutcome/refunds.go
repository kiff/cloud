package tooloutcome

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/pgschema"
)

// Refunds governed by thune (the gateway's refund module, thune step one)
// keep their money's state in gateway.thune_adjustments: the processor's
// own word, read by the gateway as the agent or brought by its events. A
// refund KIFF sent that failed or was cancelled at the processor moved no
// money, so the room it drew on its Card comes back, and its owner is told;
// so are the owners of a charge KIFF refunded that was also refunded
// outside KIFF. Both are read here from those rows, never from a caller.

// SourceRefundFailed is the evidence of a refund KIFF sent that the
// processor says failed or was cancelled.
const SourceRefundFailed = "thune:refund_failed"

// SourceOwnerNo is the release the owner's "No" to "Did this happen?"
// gives a call's Card room back by.
const SourceOwnerNo = "owner:did_this_happen"

// SourceRefundFound is the evidence of a refund the owner said didn't
// happen that the processor then listed by its marker: the room the "No"
// gave back is taken again.
const SourceRefundFound = "thune:refund_found"

// Refund notice kinds, as the alerts' refund_notice carries them.
const (
	NoticeRefundFailed  = "refund_failed"
	NoticeRefundedTwice = "refunded_twice"
	// NoticeRefundAfterNo: a refund the owner said didn't happen is at the
	// processor after all. Its room on the Card was taken again, and its
	// request may have been refunded again meanwhile.
	NoticeRefundAfterNo = "refund_after_no"
)

//go:embed sql/refund_notices.sql
var refundNoticesSQL string

// ApplyRefundSchema creates the notices' table. Idempotent.
func ApplyRefundSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgschema.Apply(ctx, pool, "tooloutcome.refund_notices", refundNoticesSQL)
}

// hasThune reports whether the gateway's thune tables are there.
func (p *Postgres) hasThune(ctx context.Context) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='gateway' AND table_name='thune_adjustments')`).Scan(&exists)
	return exists, err
}

// refundFailed is the evidence of a sent refund whose money failed.
func (p *Postgres) refundFailed(ctx context.Context, tenant, proposal string) (mandate.NoEffectEvidence, bool, error) {
	if ok, err := p.hasThune(ctx); err != nil || !ok {
		return mandate.NoEffectEvidence{}, false, err
	}
	var agent, action, fin, refund string
	var at time.Time
	err := p.pool.QueryRow(ctx, `SELECT i.agent_id, i.tool, a.financial, a.refund_id, a.updated_at
		FROM gateway.thune_adjustments a JOIN gateway.invocations i ON i.key = a.key AND i.tenant_id = a.tenant_id
		WHERE a.tenant_id=$1 AND a.key=$2 AND a.execution='sent' AND a.financial IN ('failed','cancelled') AND a.refund_id <> ''`,
		tenant, proposal).Scan(&agent, &action, &fin, &refund, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return mandate.NoEffectEvidence{}, false, nil
	}
	if err != nil {
		return mandate.NoEffectEvidence{}, false, err
	}
	return mandate.NoEffectEvidence{TenantID: tenant, ProposalID: proposal, Subject: agent, Action: action, Source: SourceRefundFailed,
		EvidenceID: refund, Reason: "the processor says the refund " + map[string]string{"failed": "failed", "cancelled": "was cancelled"}[fin], At: at}, true, nil
}

// EffectReleaser gives a call's Card room back on trusted evidence
// (mandate.EffectReleaseStore's release).
type EffectReleaser interface {
	ReleaseEffects(context.Context, mandate.NoEffectEvidence) ([]mandate.EffectRelease, error)
}

// RefundNotifier tells an account's owners about one refund: the call, the
// tool, the agent that made it and the notice's kind. The API server hands
// it the alerts, so this package holds no alert code of its own.
type RefundNotifier interface {
	NotifyRefund(tenant, call, tool, agent, kind string, at time.Time)
}

// RefundWatch gives back the room of refunds that failed at the processor
// and tells their owners, and tells the owners of charges also refunded
// outside KIFF. Each once: a release is one per draw, and a notice one per
// call and kind.
type RefundWatch struct {
	Outcomes *Postgres
	Releases EffectReleaser
	// Reinstates takes back room the owner's "No" gave back, once the
	// processor lists that refund; nil leaves it (and sends no notice).
	Reinstates mandate.EffectReinstateStore
	// Alerts is nil when no notice is sent; the room still comes back.
	Alerts RefundNotifier
	Logger *slog.Logger
	Now    func() time.Time
	// Batch is how many rows of each kind one run takes. Zero: 50.
	Batch int
}

func (w *RefundWatch) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// RunOnce does one pass and returns how many rooms came back and how many
// notices were sent.
func (w *RefundWatch) RunOnce(ctx context.Context) (released, told int, err error) {
	p := w.Outcomes
	if ok, err := p.hasThune(ctx); err != nil || !ok {
		return 0, 0, err
	}
	n := w.Batch
	if n <= 0 {
		n = 50
	}
	// Failed refunds whose draws haven't all come back.
	rows, err := p.pool.Query(ctx, `SELECT a.tenant_id, a.key FROM gateway.thune_adjustments a
		WHERE a.execution='sent' AND a.financial IN ('failed','cancelled')
		  AND EXISTS (SELECT 1 FROM cloud_meta.mandate_draws d WHERE d.tenant_id=a.tenant_id AND d.proposal_id=a.key
		      AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_releases r WHERE r.tenant_id=d.tenant_id AND r.mandate_id=d.mandate_id
		          AND r.proposal_id=d.proposal_id AND r.quantity_key=d.quantity_key))
		ORDER BY a.updated_at LIMIT $1`, n)
	if err != nil {
		return 0, 0, err
	}
	type call struct{ tenant, key string }
	failed, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (call, error) {
		var c call
		return c, r.Scan(&c.tenant, &c.key)
	})
	if err != nil {
		return 0, 0, err
	}
	for _, c := range failed {
		e, found, err := p.refundFailed(ctx, c.tenant, c.key)
		if err != nil {
			return released, told, err
		}
		if !found {
			continue
		}
		rs, err := w.Releases.ReleaseEffects(ctx, e)
		if err != nil {
			// The next pass asks again; nothing else depends on it.
			w.log(ctx, "tooloutcome: a failed refund's room didn't come back", c.key, err)
			continue
		}
		if len(rs) > 0 {
			released++
		}
	}
	// Refunds the owner said didn't happen that the processor lists by
	// their marker: the room the "No" gave back is taken again, then the
	// owner is told (the notice below, once).
	if w.Reinstates != nil {
		rows, err = p.pool.Query(ctx, `SELECT a.tenant_id, a.key, a.refund_id, a.updated_at FROM gateway.thune_adjustments a
			WHERE a.execution='sent' AND a.financial IN ('pending','succeeded') AND a.refund_id <> ''
			  AND EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_releases r WHERE r.tenant_id=a.tenant_id AND r.proposal_id=a.key AND r.source=$2
			      AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_surcharges s WHERE s.tenant_id=r.tenant_id AND s.mandate_id=r.mandate_id
			          AND s.proposal_id=r.proposal_id AND s.quantity_key=r.quantity_key))
			ORDER BY a.updated_at LIMIT $1`, n, SourceOwnerNo)
		if err != nil {
			return released, told, err
		}
		type found struct {
			tenant, key, refund string
			at                  time.Time
		}
		founds, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (found, error) {
			var f found
			return f, r.Scan(&f.tenant, &f.key, &f.refund, &f.at)
		})
		if err != nil {
			return released, told, err
		}
		for _, f := range founds {
			if _, err := w.Reinstates.ReinstateEffects(ctx, mandate.ReinstateEvidence{TenantID: f.tenant, ProposalID: f.key,
				Undoes: SourceOwnerNo, Source: SourceRefundFound, EvidenceID: f.refund, At: f.at}); err != nil {
				w.log(ctx, "tooloutcome: a refund found after the owner's No didn't take its room again", f.key, err)
			}
		}
	}
	// Notices: failed refunds, KIFF's refunds on a charge that also has a
	// counting refund made outside KIFF, and refunds found after the
	// owner's "No". The last is keyed on the "No" and the refund found,
	// once every draw the "No" released carries a surcharge: the room taken
	// again, or (rarely) a surcharge of another kind that already held the
	// key, so nothing was taken. taken says which; the owner is told either
	// way, and the second is logged.
	rows, err = p.pool.Query(ctx, `
		SELECT a.tenant_id, a.key, i.tool, i.agent_id, $2::text, true FROM gateway.thune_adjustments a
		  JOIN gateway.invocations i ON i.key = a.key AND i.tenant_id = a.tenant_id
		 WHERE a.execution='sent' AND a.financial IN ('failed','cancelled')
		   AND NOT EXISTS (SELECT 1 FROM cloud_meta.refund_notices n WHERE n.tenant_id=a.tenant_id AND n.proposal_id=a.key AND n.kind=$2)
		UNION ALL
		SELECT a.tenant_id, a.key, i.tool, i.agent_id, $3::text, true FROM gateway.thune_adjustments a
		  JOIN gateway.invocations i ON i.key = a.key AND i.tenant_id = a.tenant_id
		 WHERE a.execution='sent' AND a.financial IN ('none','pending','succeeded')
		   AND EXISTS (SELECT 1 FROM gateway.thune_refunds o WHERE o.tenant_id=a.tenant_id AND o.account=a.account AND o.object=a.object
		       AND o.origin='outside' AND o.status IN ('pending','succeeded'))
		   AND NOT EXISTS (SELECT 1 FROM cloud_meta.refund_notices n WHERE n.tenant_id=a.tenant_id AND n.proposal_id=a.key AND n.kind=$3)
		UNION ALL
		SELECT a.tenant_id, a.key, i.tool, i.agent_id, $4::text,
		       NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_releases r JOIN cloud_meta.mandate_effect_surcharges s
		           ON s.tenant_id=r.tenant_id AND s.mandate_id=r.mandate_id AND s.proposal_id=r.proposal_id AND s.quantity_key=r.quantity_key
		          WHERE r.tenant_id=a.tenant_id AND r.proposal_id=a.key AND r.source=$6 AND s.source<>$5)
		  FROM gateway.thune_adjustments a
		  JOIN gateway.invocations i ON i.key = a.key AND i.tenant_id = a.tenant_id
		 WHERE a.execution='sent' AND a.financial IN ('pending','succeeded') AND a.refund_id <> ''
		   AND EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_releases r WHERE r.tenant_id=a.tenant_id AND r.proposal_id=a.key AND r.source=$6)
		   AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_releases r WHERE r.tenant_id=a.tenant_id AND r.proposal_id=a.key AND r.source=$6
		       AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_effect_surcharges s WHERE s.tenant_id=r.tenant_id AND s.mandate_id=r.mandate_id
		           AND s.proposal_id=r.proposal_id AND s.quantity_key=r.quantity_key))
		   AND NOT EXISTS (SELECT 1 FROM cloud_meta.refund_notices n WHERE n.tenant_id=a.tenant_id AND n.proposal_id=a.key AND n.kind=$4)
		 LIMIT $1`, n, NoticeRefundFailed, NoticeRefundedTwice, NoticeRefundAfterNo, SourceRefundFound, SourceOwnerNo)
	if err != nil {
		return released, told, err
	}
	type notice struct {
		tenant, key, tool, agent, kind string
		taken                          bool
	}
	notices, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (notice, error) {
		var x notice
		return x, r.Scan(&x.tenant, &x.key, &x.tool, &x.agent, &x.kind, &x.taken)
	})
	if err != nil {
		return released, told, err
	}
	for _, x := range notices {
		var first bool
		err := p.pool.QueryRow(ctx, `INSERT INTO cloud_meta.refund_notices (tenant_id, proposal_id, kind, sent_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT DO NOTHING RETURNING true`, x.tenant, x.key, x.kind, w.now()).Scan(&first)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return released, told, err
		}
		if !x.taken {
			w.log(ctx, "tooloutcome: a refund found after the owner's No kept a surcharge of another kind, so its room wasn't taken again",
				x.key, errors.New("surcharge key already held"))
		}
		if w.Alerts != nil {
			w.Alerts.NotifyRefund(x.tenant, x.key, x.tool, x.agent, x.kind, w.now().UTC())
		}
		told++
	}
	return released, told, nil
}

// Run passes every interval until ctx ends.
func (w *RefundWatch) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, _, err := w.RunOnce(ctx); err != nil {
			w.log(ctx, "tooloutcome: the refund watch failed", "", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *RefundWatch) log(ctx context.Context, msg, key string, err error) {
	if w.Logger != nil {
		w.Logger.WarnContext(ctx, msg, slog.String("proposal", key), slog.String("err", err.Error()))
	}
}

// OwnerSaidNotSent records, in thune, the owner's signed "No" to "Did this
// happen?" on a governed refund whose outcome was unknown: the call ends
// not sent, so it no longer counts as pending on its charge, and its
// request's claim is released for another taf. This is the one write
// apps/api makes to thune's rows, and only on that answer; the gateway
// still records the refund as sent if the processor later lists it by its
// marker. A call thune doesn't govern, or one no longer unknown, changes
// nothing.
func (p *Postgres) OwnerSaidNotSent(ctx context.Context, tenant, key string) error {
	var present bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('gateway.thune_adjustments') IS NOT NULL`).Scan(&present); err != nil || !present {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := p.clock()
	var account, object, kind, request string
	err = tx.QueryRow(ctx, `UPDATE gateway.thune_adjustments SET execution='not_sent', updated_at=$3
		WHERE tenant_id=$1 AND key=$2 AND execution='unknown' AND financial='none'
		RETURNING account, object, kind, request`, tenant, key, now).Scan(&account, &object, &kind, &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if request != "" {
		if _, err := tx.Exec(ctx, `UPDATE gateway.thune_claims SET released_at=$7
			WHERE tenant_id=$1 AND account=$2 AND object=$3 AND kind=$4 AND request=$5 AND holder=$6 AND released_at IS NULL`,
			tenant, account, object, kind, request, key, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
