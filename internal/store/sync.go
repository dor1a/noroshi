package store

import (
	"context"
	"time"

	"github.com/dor1a/noroshi/internal/config"
)

// SyncConfig makes the page, group, component, and check rows match the
// YAML file. Rows are matched by key. Anything no longer in the file is
// archived rather than deleted so its history remains.
func (s *Store) SyncConfig(ctx context.Context, f *config.File) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockSync); err != nil {
		return err
	}

	var pageIDs, groupIDs, compIDs, checkIDs []int64
	for pi, p := range f.Pages {
		title, err := jsonString(p.Title)
		if err != nil {
			return err
		}
		var pageID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO page (key, title, timezone, position, archived_at)
			VALUES ($1, $2, $3, $4, NULL)
			ON CONFLICT (key) DO UPDATE
			SET title = EXCLUDED.title, timezone = EXCLUDED.timezone,
			    position = EXCLUDED.position, archived_at = NULL
			RETURNING id`, p.Key, title, p.Timezone, pi).Scan(&pageID); err != nil {
			return err
		}
		pageIDs = append(pageIDs, pageID)

		compPos := 0
		for gi, g := range p.Groups {
			name, err := jsonString(g.Name)
			if err != nil {
				return err
			}
			var groupID int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO component_group (page_id, key, name, position, archived_at)
				VALUES ($1, $2, $3, $4, NULL)
				ON CONFLICT (page_id, key) DO UPDATE
				SET name = EXCLUDED.name, position = EXCLUDED.position, archived_at = NULL
				RETURNING id`, pageID, g.Key, name, gi).Scan(&groupID); err != nil {
				return err
			}
			groupIDs = append(groupIDs, groupID)

			for _, c := range g.Components {
				cname, err := jsonString(c.Name)
				if err != nil {
					return err
				}
				desc := "{}"
				if len(c.Description) > 0 {
					if desc, err = jsonString(c.Description); err != nil {
						return err
					}
				}
				var compID int64
				if err := tx.QueryRow(ctx, `
					INSERT INTO component (page_id, group_id, key, name, description, position, archived_at)
					VALUES ($1, $2, $3, $4, $5, $6, NULL)
					ON CONFLICT (page_id, key) DO UPDATE
					SET group_id = EXCLUDED.group_id, name = EXCLUDED.name,
					    description = EXCLUDED.description, position = EXCLUDED.position,
					    archived_at = NULL
					RETURNING id`, pageID, groupID, c.Key, cname, desc, compPos).Scan(&compID); err != nil {
					return err
				}
				compPos++
				compIDs = append(compIDs, compID)

				for ki, k := range c.Checks {
					spec, err := jsonString(k)
					if err != nil {
						return err
					}
					var checkID int64
					if err := tx.QueryRow(ctx, `
						INSERT INTO checks (component_id, key, type, spec, position, archived_at)
						VALUES ($1, $2, $3, $4, $5, NULL)
						ON CONFLICT (component_id, key) DO UPDATE
						SET type = EXCLUDED.type, spec = EXCLUDED.spec,
						    position = EXCLUDED.position, archived_at = NULL
						RETURNING id`, compID, k.Key, k.Type, spec, ki).Scan(&checkID); err != nil {
						return err
					}
					checkIDs = append(checkIDs, checkID)
				}
			}
		}
	}

	now := time.Now()
	for _, a := range []struct {
		table string
		keep  []int64
	}{
		{"checks", checkIDs},
		{"component", compIDs},
		{"component_group", groupIDs},
		{"page", pageIDs},
	} {
		keep := a.keep
		if keep == nil {
			keep = []int64{}
		}
		if _, err := tx.Exec(ctx, `UPDATE `+a.table+`
			SET archived_at = $1
			WHERE archived_at IS NULL AND NOT (id = ANY($2))`, now, keep); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
