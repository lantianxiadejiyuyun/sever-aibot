package store

import (
	"context"
	"database/sql"
)

// Early development databases marked migration 0010 applied before its additive
// fields stabilized. Preserve their data; unknown historical snapshots stay empty.
func migrateAIDraftCompat(ctx context.Context, tx *sql.Tx) error {
	type column struct{ name, declaration string }
	for _, table := range []struct {
		name    string
		columns []column
	}{
		{"ai_conversations", []column{{"prompt_version", "INTEGER NOT NULL DEFAULT 0"}, {"prompt_snapshot", "TEXT NOT NULL DEFAULT ''"}}},
		{"ai_messages", []column{{"served_model_id", "TEXT NOT NULL DEFAULT ''"}, {"cost", "REAL"}, {"price_snapshot", "TEXT NOT NULL DEFAULT ''"}}},
	} {
		rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table.name+")")
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var defaultValue any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
				rows.Close()
				return err
			}
			existing[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, column := range table.columns {
			if !existing[column.name] {
				// Identifiers and declarations come exclusively from the fixed list above.
				if _, err = tx.ExecContext(ctx, "ALTER TABLE "+table.name+" ADD COLUMN "+column.name+" "+column.declaration); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
