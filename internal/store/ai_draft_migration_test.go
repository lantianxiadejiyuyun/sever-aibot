package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAIDraftMigrationPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`DELETE FROM schema_migrations WHERE version=16`,
		`DROP TABLE ai_prompt_versions`,
		`ALTER TABLE ai_conversations DROP COLUMN prompt_version`,
		`ALTER TABLE ai_conversations DROP COLUMN prompt_snapshot`,
		`ALTER TABLE ai_messages DROP COLUMN served_model_id`,
		`ALTER TABLE ai_messages DROP COLUMN cost`,
		`ALTER TABLE ai_messages DROP COLUMN price_snapshot`,
		`INSERT INTO admins(id,username,password_hash,created_at,updated_at) VALUES('a','migration-test','fixture','2026-01-01','2026-01-01')`,
		`INSERT INTO ai_configs(id,kind,body) VALUES('m','models','{}')`,
		`INSERT INTO ai_conversations(id,owner_id,title,model_id,created_at) VALUES('c','a','Preserve me','m','2026-01-01')`,
		`INSERT INTO ai_messages(conversation_id,role,content,status,created_at) VALUES('c','assistant','Historical output','completed','2026-01-01')`,
	}
	for _, q := range statements {
		if _, err = s.db.Exec(q); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	s.Close()
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var content, served, snapshot string
		var cost any
		err = s.db.QueryRow(`SELECT content,served_model_id,price_snapshot,cost FROM ai_messages WHERE conversation_id='c'`).Scan(&content, &served, &snapshot, &cost)
		if err != nil || content != "Historical output" || served != "" || snapshot != "" || cost != nil {
			s.Close()
			t.Fatalf("historical data misrepresented: %q %q %q %v %v", content, served, snapshot, cost, err)
		}
		var version int
		if err = s.db.QueryRowContext(context.Background(), `SELECT prompt_version FROM ai_conversations WHERE id='c'`).Scan(&version); err != nil || version != 0 {
			s.Close()
			t.Fatalf("prompt default: %d %v", version, err)
		}
		s.Close()
	}
}
