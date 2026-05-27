package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()

	// Should BLOCK: hardcoded AWS key in source.
	write(t, dir, "app.py", `client = boto3.client("s3", aws_access_key_id="AKIAABCDEFGHIJKLMNOP")`)
	// Should BLOCK: remote DB URL with creds.
	write(t, dir, "config.js", `const db = "postgresql://admin:s3cr3tpw@db.prod.rds.amazonaws.com:5432/app"`)
	// Should NOT block: .env is the sanctioned secret channel.
	write(t, dir, ".env", `OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789`)
	// Should NOT block: localhost dev string.
	write(t, dir, "dev.js", `const url = "postgres://postgres:postgres@localhost:5432/dev"`)
	// Should WARN: Supabase dependency.
	write(t, dir, "package.json", `{"dependencies":{"@supabase/supabase-js":"^2.0.0","express":"^4"}}`)
	// Should WARN: external host in source.
	write(t, dir, "client.ts", `const SUPA = "https://xyzcompany.supabase.co"`)
	// Skipped dir.
	write(t, dir, "node_modules/pkg/index.js", `const k = "AKIAIOSFODNN7FAKEKEYY"`)

	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}

	if len(res.Secrets) != 2 {
		t.Errorf("expected 2 secret findings, got %d: %+v", len(res.Secrets), res.Secrets)
	}
	for _, s := range res.Secrets {
		if s.File == ".env" || s.File == "dev.js" {
			t.Errorf("should not flag %s as secret: %+v", s.File, s)
		}
	}

	// External: supabase dep + supabase host = 2.
	if len(res.External) != 2 {
		t.Errorf("expected 2 external findings, got %d: %+v", len(res.External), res.External)
	}
}
