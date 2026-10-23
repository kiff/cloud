// Package events records which domain each stored event belongs to, so a
// runtime can rebuild its entities' state from its own domain's history.
//
// The framework keeps entity state in the runtime's in-memory state
// machine and stores events in kiff_events, which has no domain
// dimension: in the framework one runtime is one domain. The cloud keeps
// every domain of a tenant in one schema, and rebuilds runtimes on every
// restart and cache reset. Replaying kiff_events by entity id alone would
// let one domain's events move another domain's entity with the same id,
// so ownership is recorded beside each event at write time.
//
// The side table is cloud-owned (kiff_events is untouched) and written
// in the same transaction as the event, so there is no window in which a
// stored event has no recorded domain. Events written before this
// existed have no row; they are not replayed, because guessing their
// domain is the retroactive-reattribution defect domain scoping rejects.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
)

// schemaSQL creates the domain-ownership table in the tenant schema the
// connection's search_path points at. Idempotent; a new table takes no
// lock on kiff_events, so it is safe to apply against a live schema.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS cloud_event_domains (
    event_id  TEXT PRIMARY KEY REFERENCES kiff_events(id) ON DELETE CASCADE,
    domain_id TEXT NOT NULL,
    entity_id TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS cloud_event_domains_entity_idx
    ON cloud_event_domains (domain_id, entity_id);
`

// SchemaSQL returns the migration as raw DDL, for provisioning to apply
// in the same transaction as the framework schema.
func SchemaSQL() string { return schemaSQL }

// ApplySchema applies the migration through pool, whose search_path must
// be pinned to the tenant schema. The resolver calls it once per tenant
// per process, which also brings tenants provisioned before this table up to
// date.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("events: apply schema: %w", err)
	}
	return nil
}

// DomainStore implements the framework's event.Store for one (tenant,
// domain). Append stamps the domain; List is the framework store's
// tenant-wide read, unchanged, so timelines and lifecycle views see what
// they saw before. History is the domain-scoped read replay uses.
type DomainStore struct {
	pool     *pgxpool.Pool
	domainID string
	reads    kiffevent.Store

	// BeforeAppend runs before an event is stored. The resolver sets it
	// to the state machine's hydration, so an entity's history is
	// replayed before its next event is written and never includes it:
	// the framework appends an event before applying it to state.
	BeforeAppend func(ctx context.Context, entityID string) error
}

// NewDomainStore binds a store to one domain. reads serves List.
func NewDomainStore(pool *pgxpool.Pool, domainID string, reads kiffevent.Store) (*DomainStore, error) {
	if pool == nil || reads == nil {
		return nil, errors.New("events: pool and reads are required")
	}
	if domainID == "" {
		return nil, errors.New("events: domain id is required for a domain-scoped store")
	}
	return &DomainStore{pool: pool, domainID: domainID, reads: reads}, nil
}

// Append stores the event and its domain in one transaction. The event
// columns match the framework's own insert.
func (s *DomainStore) Append(ctx context.Context, e kiffevent.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if s.BeforeAppend != nil {
		if err := s.BeforeAppend(ctx, e.EntityID); err != nil {
			return err
		}
	}
	metadata, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	payload := []byte("{}")
	if e.Payload != nil {
		if payload, err = json.Marshal(e.Payload); err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("events: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO kiff_events (
			id, type, entity_id, entity_type, source, actor_id,
			occurred_at, metadata, payload
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		e.ID, e.Type, e.EntityID, e.EntityType, e.Source, e.ActorID,
		e.OccurredAt.UTC(), metadata, payload,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cloud_event_domains (event_id, domain_id, entity_id)
		VALUES ($1, $2, $3)
	`, e.ID, s.domainID, e.EntityID); err != nil {
		return fmt.Errorf("events: record domain: %w", err)
	}
	return tx.Commit(ctx)
}

// List is the framework store's read: every event for the entity in the
// tenant, in insertion order.
func (s *DomainStore) List(ctx context.Context, entityID string) ([]kiffevent.Event, error) {
	return s.reads.List(ctx, entityID)
}

// History returns the entity's events recorded under this store's
// domain, in insertion order. Events with no recorded domain are left
// out.
func (s *DomainStore) History(ctx context.Context, entityID string) ([]kiffevent.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.type, e.entity_id, e.entity_type, e.source, e.actor_id,
		       e.occurred_at, e.metadata, e.payload
		FROM kiff_events e
		JOIN cloud_event_domains d ON d.event_id = e.id
		WHERE d.domain_id = $1 AND d.entity_id = $2
		ORDER BY e.inserted_at, e.id
	`, s.domainID, entityID)
	if err != nil {
		return nil, fmt.Errorf("events: query history: %w", err)
	}
	defer rows.Close()

	var out []kiffevent.Event
	for rows.Next() {
		var (
			ev          kiffevent.Event
			metaJSON    []byte
			payloadJSON []byte
		)
		if err := rows.Scan(
			&ev.ID, &ev.Type, &ev.EntityID, &ev.EntityType, &ev.Source, &ev.ActorID,
			&ev.OccurredAt, &metaJSON, &payloadJSON,
		); err != nil {
			return nil, fmt.Errorf("events: scan: %w", err)
		}
		if len(metaJSON) > 0 {
			if err := json.Unmarshal(metaJSON, &ev.Metadata); err != nil {
				return nil, fmt.Errorf("events: unmarshal metadata: %w", err)
			}
		}
		// As the framework reads it: an empty object is a nil payload.
		if p := string(payloadJSON); p != "" && p != "null" && p != "{}" {
			if err := json.Unmarshal(payloadJSON, &ev.Payload); err != nil {
				return nil, fmt.Errorf("events: unmarshal payload: %w", err)
			}
		}
		ev.OccurredAt = ev.OccurredAt.UTC()
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: iterate: %w", err)
	}
	return out, nil
}
