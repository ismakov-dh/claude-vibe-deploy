package cmd

import (
	"os"
	"testing"

	"github.com/vibe-deploy/vd/internal/state"
)

func TestManifestForDestroy(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())

	if _, e := manifestForDestroy("../etc"); e == nil || e.Code != "INVALID_NAME" {
		t.Fatalf("path-like name accepted: %v", e)
	}
	if _, e := manifestForDestroy("ghost"); e == nil || e.Code != "NOT_FOUND" {
		t.Fatalf("missing app: %v", e)
	}

	// A failed first deploy: directory and compose file, no manifest.
	if err := os.MkdirAll(state.AppSrcDir("half"), 0755); err != nil {
		t.Fatal(err)
	}
	compose := "labels:\n  - traefik.http.routers.vd-half.middlewares=vd-half-strip-identity,authentik-fa@file,vd-half-ingress\n"
	if err := os.WriteFile(state.AppComposePath("half"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	m, e := manifestForDestroy("half")
	if e != nil || m.Name != "half" || !m.Auth || m.AuthGroup != "vibe-half" || m.MCPOAuth {
		t.Fatalf("stand-in: %+v %v", m, e)
	}

	// A manifest that exists but cannot be parsed is not guessed around.
	if err := os.WriteFile(state.AppManifestPath("half"), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, e := manifestForDestroy("half"); e == nil || e.Code != "MANIFEST_UNREADABLE" {
		t.Fatalf("unreadable manifest: %v", e)
	}
}
