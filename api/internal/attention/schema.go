// Package attention maintains durable work independently of notification delivery.
package attention

// SchemaSQL is a cloud-owned extension installed inside each tenant schema.
// Triggers make capture atomic with existing framework writes, including callers
// outside the HTTP handler. No framework source or table columns are changed.
// Approval work is projected synchronously so required reviews are visible even
// when every background worker is stopped. Blocked decisions use the outbox.
func SchemaSQL() string { return schemaSQL }

const schemaSQL = `
CREATE TABLE IF NOT EXISTS cloud_attention (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind IN ('approval','blocked','card_warning')),
 source_id text NOT NULL,
 domain_id text NOT NULL DEFAULT '',
 actor_id text NOT NULL DEFAULT '',
 action text NOT NULL,
 entity_id text NOT NULL,
 reason text NOT NULL DEFAULT '',
 status text NOT NULL CHECK (status IN ('open','resolved')),
 assignee text NOT NULL DEFAULT '',
 resolved_by text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 revision bigint NOT NULL DEFAULT 1
);
ALTER TABLE cloud_attention DROP CONSTRAINT IF EXISTS cloud_attention_kind_check;
ALTER TABLE cloud_attention ADD CONSTRAINT cloud_attention_kind_check CHECK (kind IN ('approval','blocked','card_warning'));
-- decided_at is when the decision behind a blocked item was made, so a
-- late-processed older decision never overwrites a newer one.
ALTER TABLE cloud_attention ADD COLUMN IF NOT EXISTS decided_at timestamptz;
CREATE INDEX IF NOT EXISTS cloud_attention_open_idx ON cloud_attention(status, created_at, id);
CREATE TABLE IF NOT EXISTS cloud_attention_seen (
 subject text NOT NULL,
 item_id text NOT NULL REFERENCES cloud_attention(id) ON DELETE CASCADE,
 revision bigint NOT NULL,
 PRIMARY KEY(subject,item_id)
);
CREATE TABLE IF NOT EXISTS cloud_attention_history (
 id bigserial PRIMARY KEY,
 item_id text NOT NULL REFERENCES cloud_attention(id) ON DELETE CASCADE,
 actor text NOT NULL,
 action text NOT NULL,
 revision bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS cloud_attention_events (
 id bigserial PRIMARY KEY,
 event_key text NOT NULL UNIQUE,
 payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS cloud_attention_jobs (
 event_id bigint NOT NULL REFERENCES cloud_attention_events(id) ON DELETE CASCADE,
 consumer text NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(event_id,consumer)
);
CREATE INDEX IF NOT EXISTS cloud_attention_jobs_pending_idx
 ON cloud_attention_jobs(consumer,available_at,event_id) WHERE completed_at IS NULL;

CREATE OR REPLACE FUNCTION cloud_capture_attention_decision() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE eid bigint; src text; body jsonb;
BEGIN
 -- A refusal that is the owner's answer to a held action (they rejected
 -- it, or nobody answered before the hold expired) is not open work: the
 -- hold is settled on Needs you, where it shows as Rejected or Expired.
 -- Every other blocked decision opens a work item.
 IF NEW.kind = 'proposal_decision' AND NEW.data->>'outcome' = 'blocked'
    AND COALESCE((NEW.data->'reasons')::text,'') NOT IN ('["mandate_owner_rejected"]','["mandate_owner_expired"]') THEN
  -- One proposal can write several decision records (trace_id is the
  -- proposal id). The work item is keyed by proposal; the event is keyed
  -- by proposal plus decision content, so a re-delivery of the same
  -- decision is one event while a materially different re-decision is a
  -- new event that updates the item.
  src := COALESCE(NULLIF(NEW.trace_id,''), NEW.id);
  body := jsonb_build_object('domain_id',COALESCE(NEW.data->>'domain_id',''),
    'actor_id',COALESCE(NEW.actor_id,''),'action',COALESCE(NEW.data->>'action',''),
    'entity_id',NEW.entity_id,'reason',COALESCE((NEW.data->'reasons')::text,''));
  INSERT INTO cloud_attention_events(event_key,payload)
   VALUES ('decision:' || src || ':' || md5(body::text), body || jsonb_build_object(
    'source_id',src,'proposal_id',NEW.trace_id,'created_at',NEW.created_at))
   ON CONFLICT DO NOTHING RETURNING id INTO eid;
  IF eid IS NOT NULL THEN
   INSERT INTO cloud_attention_jobs(event_id,consumer) VALUES(eid,'attention');
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER cloud_capture_attention_decision
 AFTER INSERT ON kiff_audit FOR EACH ROW EXECUTE FUNCTION cloud_capture_attention_decision();

CREATE OR REPLACE FUNCTION cloud_capture_attention_approval() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
 INSERT INTO cloud_attention(id,kind,source_id,domain_id,actor_id,action,entity_id,reason,status,created_at)
 -- domain_id is read through to_jsonb: tenants provisioned before domains
 -- have no such column until kiff-tenant migrates them, and a direct
 -- NEW.domain_id reference would fail every approval write there.
 VALUES ('approval:'||NEW.id,'approval',NEW.id,COALESCE(to_jsonb(NEW)->>'domain_id',''),NEW.requested_by,
 NEW.action_name,NEW.entity_id,COALESCE(NEW.reason,''),
 CASE WHEN NEW.status='pending' THEN 'open' ELSE 'resolved' END,NEW.created_at)
 ON CONFLICT(id) DO UPDATE SET
  status=EXCLUDED.status, reason=EXCLUDED.reason, updated_at=now(),
  resolved_by=COALESCE(NEW.reviewed_by,''), revision=cloud_attention.revision+1
 WHERE cloud_attention.status IS DISTINCT FROM EXCLUDED.status
    OR cloud_attention.reason IS DISTINCT FROM EXCLUDED.reason;
 RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER cloud_capture_attention_approval
 AFTER INSERT OR UPDATE ON kiff_approvals FOR EACH ROW EXECUTE FUNCTION cloud_capture_attention_approval();

-- Items opened before this rule for holds the owner already answered are
-- resolved, with a history row so the change is on the record.
WITH settled AS (
 UPDATE cloud_attention SET status='resolved', resolved_by='kiff:hold-answered',
  revision=revision+1, updated_at=now()
 WHERE kind='blocked' AND status='open'
   AND reason IN ('["mandate_owner_rejected"]','["mandate_owner_expired"]')
 RETURNING id, revision)
INSERT INTO cloud_attention_history(item_id,actor,action,revision)
 SELECT id,'kiff:hold-answered','resolve',revision FROM settled;

-- Existing pending work must not disappear when the new view is enabled.
INSERT INTO cloud_attention(id,kind,source_id,domain_id,actor_id,action,entity_id,reason,status,created_at)
 SELECT 'approval:'||a.id,'approval',a.id,COALESCE(to_jsonb(a)->>'domain_id',''),a.requested_by,a.action_name,a.entity_id,COALESCE(a.reason,''),'open',a.created_at
 FROM kiff_approvals a WHERE a.status='pending'
 ON CONFLICT DO NOTHING;
`
