package cmd

import (
	"os"
	"testing"

	"github.com/vibe-deploy/vd/internal/state"
)

func TestMCPPasswordSurvivesRedeploy(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	if err := os.MkdirAll(state.AppDir("app"), 0755); err != nil {
		t.Fatal(err)
	}
	first := mcpPasswordFor("app", false)
	if len(first) < 32 {
		t.Fatalf("new password too short: %q", first)
	}
	if err := writeMCPEnv("app", "postgres://ro:x@vd-postgres/app", mcpUser, first); err != nil {
		t.Fatal(err)
	}
	if again := mcpPasswordFor("app", false); again != first {
		t.Fatal("redeploy issued a new Basic password: every `claude mcp add --header` client is cut off")
	}
	if rotated := mcpPasswordFor("app", true); rotated == first || len(rotated) < 32 {
		t.Fatal("--mcp-rotate-password kept the old password")
	}
}
