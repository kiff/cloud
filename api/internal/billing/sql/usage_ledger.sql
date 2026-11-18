-- Billable governed-operation ledger. One idempotent row per
-- billable operation; the Stripe meter-events reporter (a later slice)
-- drains rows where reported_at IS NULL and stamps it on success, so a
-- meter event is emitted exactly once per operation.
CREATE TABLE IF NOT EXISTS cloud_meta.billing_usage_events (
    operation_id text PRIMARY KEY,
    tenant_id    text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    outcome      text NOT NULL,
    occurred_at  timestamptz NOT NULL,
    reported_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- The reporter scans unreported rows per tenant in time order.
CREATE INDEX IF NOT EXISTS billing_usage_events_unreported_idx
    ON cloud_meta.billing_usage_events (tenant_id, occurred_at)
    WHERE reported_at IS NULL;
