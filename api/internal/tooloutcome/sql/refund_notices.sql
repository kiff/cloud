-- The owner's notices about refunds (thune step one), once each: a refund
-- KIFF sent that failed or was cancelled at the processor, and a charge
-- KIFF refunded that was also refunded outside KIFF. Read from the
-- gateway's own thune rows, never from a caller's word.
CREATE TABLE IF NOT EXISTS cloud_meta.refund_notices (
    tenant_id  TEXT NOT NULL,
    proposal_id TEXT NOT NULL,
    kind       TEXT NOT NULL,
    sent_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, proposal_id, kind)
);
