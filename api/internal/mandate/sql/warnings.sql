-- Warning work is captured with accounting, delivered after commit.
-- It has no authority/approval columns and uses separate notification locks.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_warning_settings (
 tenant_id text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
 mandate_id text NOT NULL,
 revision bigint NOT NULL,
 changed_at timestamptz NOT NULL DEFAULT now(),
 changed_by text NOT NULL,
 settings jsonb NOT NULL,
 PRIMARY KEY(tenant_id,mandate_id,revision),
 FOREIGN KEY(tenant_id,mandate_id) REFERENCES cloud_meta.mandates(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_warning_work (
 id bigserial PRIMARY KEY,
 tenant_id text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
 mandate_id text NOT NULL,
 observed_at timestamptz NOT NULL,
 available_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 completed_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 card jsonb NOT NULL,
 transaction_key text,
 FOREIGN KEY(tenant_id,mandate_id) REFERENCES cloud_meta.mandates(tenant_id,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS mandate_warning_work_pending ON cloud_meta.mandate_warning_work(available_at,id) WHERE completed_at IS NULL;
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_warning_episodes (
 tenant_id text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
 mandate_id text NOT NULL,
 limit_key text NOT NULL,
 state jsonb NOT NULL,
 observed_at timestamptz NOT NULL,
 PRIMARY KEY(tenant_id,mandate_id,limit_key),
 FOREIGN KEY(tenant_id,mandate_id) REFERENCES cloud_meta.mandates(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_warnings (
 tenant_id text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
 mandate_id text NOT NULL,
 id text NOT NULL,
 recorded_at timestamptz NOT NULL,
 body jsonb NOT NULL,
 recipients jsonb,
 resolved_at timestamptz,
 attention_dirty boolean NOT NULL DEFAULT true,
 attention_delivered_at timestamptz,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,mandate_id) REFERENCES cloud_meta.mandates(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_warning_deliveries (
 tenant_id text NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
 event_id text NOT NULL,
 destination text NOT NULL,
 delivered_at timestamptz,
 cancelled_at timestamptz,
 attempts integer NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(),
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(tenant_id,event_id,destination),
 FOREIGN KEY(tenant_id,event_id) REFERENCES cloud_meta.mandate_warnings(tenant_id,id) ON DELETE CASCADE
);

ALTER TABLE cloud_meta.mandate_warning_work ADD COLUMN IF NOT EXISTS transaction_key text;
CREATE UNIQUE INDEX IF NOT EXISTS mandate_warning_work_transaction ON cloud_meta.mandate_warning_work(tenant_id,mandate_id,transaction_key);
CREATE INDEX IF NOT EXISTS mandate_warning_work_card_pending ON cloud_meta.mandate_warning_work(tenant_id,mandate_id,id) WHERE completed_at IS NULL;
ALTER TABLE cloud_meta.mandate_warnings ADD COLUMN IF NOT EXISTS resolved_at timestamptz, ADD COLUMN IF NOT EXISTS attention_dirty boolean NOT NULL DEFAULT true;
ALTER TABLE cloud_meta.mandate_warnings
 ADD COLUMN IF NOT EXISTS delivery_available_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN IF NOT EXISTS delivery_lease_until timestamptz,
 ADD COLUMN IF NOT EXISTS delivery_attempts bigint NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS delivery_error text NOT NULL DEFAULT '';
ALTER TABLE cloud_meta.mandate_warning_deliveries ADD COLUMN IF NOT EXISTS cancelled_at timestamptz;

CREATE OR REPLACE FUNCTION cloud_meta.capture_card_warning() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE observed timestamptz := clock_timestamp(); card_id text;
BEGIN
 IF TG_TABLE_NAME='mandate_draws' THEN observed:=GREATEST(observed,NEW.drawn_at); END IF;
 IF TG_TABLE_NAME='mandate_effect_releases' THEN observed:=GREATEST(observed,NEW.recorded_at); END IF;
 IF TG_TABLE_NAME='mandates' THEN card_id:=NEW.id; ELSE card_id:=NEW.mandate_id; END IF;
 INSERT INTO cloud_meta.mandate_warning_work(tenant_id,mandate_id,observed_at,card,transaction_key)
 SELECT m.tenant_id,m.id,observed,to_jsonb(m),pg_current_xact_id()::text FROM cloud_meta.mandates m
 WHERE m.tenant_id=NEW.tenant_id AND m.id=card_id
 ON CONFLICT(tenant_id,mandate_id,transaction_key) DO UPDATE SET observed_at=GREATEST(cloud_meta.mandate_warning_work.observed_at,EXCLUDED.observed_at),card=EXCLUDED.card;
 RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER capture_card_warning_draw AFTER INSERT ON cloud_meta.mandate_draws
 FOR EACH ROW EXECUTE FUNCTION cloud_meta.capture_card_warning();
CREATE OR REPLACE TRIGGER capture_card_warning_release AFTER INSERT ON cloud_meta.mandate_effect_releases
 FOR EACH ROW EXECUTE FUNCTION cloud_meta.capture_card_warning();
CREATE OR REPLACE TRIGGER capture_card_warning_card AFTER INSERT OR UPDATE ON cloud_meta.mandates
 FOR EACH ROW EXECUTE FUNCTION cloud_meta.capture_card_warning();
CREATE OR REPLACE TRIGGER capture_card_warning_settings AFTER INSERT ON cloud_meta.mandate_warning_settings
 FOR EACH ROW EXECUTE FUNCTION cloud_meta.capture_card_warning();
