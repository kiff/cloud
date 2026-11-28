package gateway

import (
	"context"
	"encoding/json"
)

// ClassRecord is what gateway.tools keeps of a tool's class (K4), for
// apps/api to fill a Card draft's openings: the class, its narrow openings,
// and whether its table has a condition that lowers it on a call
// (create_client_invoice with status draft), which a draft opens nothing
// for: a call that fixes the condition is a write already, and opening the
// whole class would let every other call through.
type ClassRecord struct {
	Class       string
	Opens       []byte
	Conditioned bool
}

// ClassRecord is the tool's ClassRecord.
func (t Tool) ClassRecord() ClassRecord {
	opens, _ := json.Marshal(t.NarrowOpenings())
	if string(opens) == "null" {
		opens = nil
	}
	return ClassRecord{Class: string(t.Class()), Opens: opens, Conditioned: t.Conditioned()}
}

// RecordClasses writes every tool's class record to gateway.tools, so a
// reviewed table that changed reaches the rows connected before it. Run at
// start; it writes only rows that differ.
func (c PostgresCatalog) RecordClasses(ctx context.Context) (int, error) {
	rows, err := c.Pool.Query(ctx, `SELECT DISTINCT tenant_id FROM gateway.tools`)
	if err != nil {
		return 0, err
	}
	var tenants []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return 0, err
		}
		tenants = append(tenants, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, tenant := range tenants {
		m, err := c.RecordTenantClasses(ctx, tenant)
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// RecordTenantClasses writes the class record of each of tenantID's tools,
// read as calls read them (the profile's money mark included). A profile
// applied runs it, so a tool's class is known from then, not from the next
// start.
func (c PostgresCatalog) RecordTenantClasses(ctx context.Context, tenantID string) (int, error) {
	tools, err := c.Tools(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tools {
		r := t.ClassRecord()
		tag, err := c.Pool.Exec(ctx, `UPDATE gateway.tools SET class = $3, class_opens = $4, class_conditioned = $5
			WHERE tenant_id = $1 AND name = $2
			  AND (class IS DISTINCT FROM $3 OR class_opens::text IS DISTINCT FROM $4::jsonb::text OR class_conditioned IS DISTINCT FROM $5)`,
			tenantID, t.Name, r.Class, r.Opens, r.Conditioned)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
