package gateway

import (
	"context"
	"encoding/json"
)

// ClassRecord is what gateway.tools keeps of a tool's class (K4): the class
// and its narrow openings, for apps/api to fill a Card draft's openings.
func (t Tool) ClassRecord() (string, []byte) {
	opens, _ := json.Marshal(t.NarrowOpenings())
	if string(opens) == "null" {
		opens = nil
	}
	return string(t.Class()), opens
}

// RecordClasses writes every tool's class and narrow openings to
// gateway.tools, so a reviewed table that changed reaches the rows connected
// before it. Run at start; it reads each account's tools as calls do (the
// profile's money mark included) and writes only rows that differ.
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
		tools, err := c.Tools(ctx, tenant)
		if err != nil {
			return n, err
		}
		for _, t := range tools {
			class, opens := t.ClassRecord()
			tag, err := c.Pool.Exec(ctx, `UPDATE gateway.tools SET class = $3, class_opens = $4
				WHERE tenant_id = $1 AND name = $2 AND (class IS DISTINCT FROM $3 OR class_opens::text IS DISTINCT FROM $4::jsonb::text)`,
				tenant, t.Name, class, opens)
			if err != nil {
				return n, err
			}
			n += int(tag.RowsAffected())
		}
	}
	return n, nil
}
