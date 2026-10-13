package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// A redeploy's src is exactly what was pushed: files the previous push had are
// gone, vd's .env stays, skipped directories are not copied.
func TestMirrorDirDropsStaleFiles(t *testing.T) {
	root := t.TempDir()
	push, src := filepath.Join(root, "push"), filepath.Join(root, "apps", "x", "src")
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(push, "app.py"), "new")
	write(filepath.Join(push, "lib", "a.py"), "a")
	write(filepath.Join(push, "node_modules", "big.js"), "x")
	write(filepath.Join(src, "app.py"), "old")
	write(filepath.Join(src, "scripts", "update_report.sh"), "password")
	write(filepath.Join(src, "lib", "gone.py"), "stale")
	write(filepath.Join(src, ".env"), "DATABASE_URL=keep")
	write(filepath.Join(src, "Dockerfile.vd"), "old")

	if err := mirrorDir(push, src); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"scripts", "lib/gone.py", "Dockerfile.vd", "node_modules"} {
		if _, err := os.Stat(filepath.Join(src, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", p, err)
		}
	}
	for p, want := range map[string]string{"app.py": "new", "lib/a.py": "a", ".env": "DATABASE_URL=keep"} {
		if b, err := os.ReadFile(filepath.Join(src, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", p, b, err, want)
		}
	}
}

// Deploying from the app's own src (or a directory holding it) would delete
// the very source being copied.
func TestMirrorDirRefusesOverlap(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "apps", "x", "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "app.py"), []byte("keep"), 0644)
	for _, from := range []string{src, filepath.Join(root, "apps")} {
		if err := mirrorDir(from, src); err == nil {
			t.Errorf("mirror from %s into %s was not refused", from, src)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "app.py")); err != nil {
		t.Fatal("source deleted")
	}
}
