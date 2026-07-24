package location

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The official Rwanda administrative hierarchy, embedded at build time (same
// dataset the mobile driver-onboarding picker uses). ~17.4k nodes.
//
//go:embed rwanda_locations.json
var rwandaLocationsJSON []byte

type rwProvince struct {
	Name      string       `json:"name"`
	Districts []rwDistrict `json:"districts"`
}
type rwDistrict struct {
	Name    string     `json:"name"`
	Sectors []rwSector `json:"sectors"`
}
type rwSector struct {
	Name  string   `json:"name"`
	Cells []rwCell `json:"cells"`
}
type rwCell struct {
	Name     string   `json:"name"`
	Villages []string `json:"villages"`
}

// SeedAdminUnits idempotently loads Rwanda's admin hierarchy into admin_units.
// No-op if already seeded (safe to call on every boot). Rows are built
// parent-before-child so the self-referencing FK holds during the bulk COPY.
func SeedAdminUnits(ctx context.Context, db *pgxpool.Pool) error {
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_units`).Scan(&count); err != nil {
		return fmt.Errorf("admin_units count: %w", err)
	}
	if count > 0 {
		return nil // already seeded
	}

	var provinces []rwProvince
	if err := json.Unmarshal(rwandaLocationsJSON, &provinces); err != nil {
		return fmt.Errorf("parse rwanda_locations.json: %w", err)
	}

	var rows [][]interface{}
	add := func(parentID *uuid.UUID, level, name, path string) uuid.UUID {
		id := uuid.New()
		var parent interface{}
		if parentID != nil {
			parent = *parentID
		}
		rows = append(rows, []interface{}{id, parent, level, name, path})
		return id
	}

	for _, p := range provinces {
		pid := add(nil, "province", p.Name, p.Name)
		for _, d := range p.Districts {
			dPath := p.Name + " > " + d.Name
			did := add(&pid, "district", d.Name, dPath)
			for _, s := range d.Sectors {
				sPath := dPath + " > " + s.Name
				sid := add(&did, "sector", s.Name, sPath)
				for _, c := range s.Cells {
					cPath := sPath + " > " + c.Name
					cid := add(&sid, "cell", c.Name, cPath)
					for _, v := range c.Villages {
						add(&cid, "village", v, cPath+" > "+v)
					}
				}
			}
		}
	}

	copied, err := db.CopyFrom(ctx,
		pgx.Identifier{"admin_units"},
		[]string{"id", "parent_id", "level", "name", "path"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("admin_units copy: %w", err)
	}
	if int(copied) != len(rows) {
		return fmt.Errorf("admin_units seed incomplete: copied %d of %d", copied, len(rows))
	}
	return nil
}
