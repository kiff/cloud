-- Billing webhook state. Lives in cloud_meta alongside the
-- other cloud-side tables. Two concerns, two tables.

-- Processed Stripe event ids — the idempotency ledger. A duplicate
-- webhook delivery (Stripe is at-least-once) is detected by a primary
-- key conflict on insert.
CREATE TABLE IF NOT EXISTS cloud_meta.billing_processed_events (
    event_id     text PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- Stripe customer -> KIFF tenant correlation, captured at activation
-- (when the checkout session carries the tenant_slug custom field) so
-- later lifecycle events that carry only a customer id resolve to one
-- tenant. A customer is owned by at most one tenant (PK on customer).
CREATE TABLE IF NOT EXISTS cloud_meta.billing_customers (
    customer_id text PRIMARY KEY,
    tenant_id   text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    linked_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS billing_customers_tenant_idx
    ON cloud_meta.billing_customers (tenant_id);
