package output

import (
	"encoding/json"
	"io"
	"os"
	"testing"
)

func capture(t *testing.T, fn func()) Response {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	var resp Response
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("not JSON: %q", b)
	}
	return resp
}

// In --json mode a warning must reach the response, not vanish.
func TestWarningsReachJSON(t *testing.T) {
	SetJSON(true)
	t.Cleanup(func() { SetJSON(false); warnings = nil })

	Warn("DB provisioning failed: %s", "boom")
	Warn("DB provisioning failed: %s", "boom") // repeated: once in the output
	resp := capture(t, func() {
		SuccessWithWarnings("deploy", map[string]any{"x": 1},
			[]string{"policy warning", "DB provisioning failed: boom"})
	})
	want := []string{"DB provisioning failed: boom", "policy warning"}
	if len(resp.Warnings) != len(want) {
		t.Fatalf("warnings = %q, want %q", resp.Warnings, want)
	}
	for i := range want {
		if resp.Warnings[i] != want[i] {
			t.Fatalf("warnings = %q, want %q", resp.Warnings, want)
		}
	}
}

func TestPlainSuccessCarriesWarnings(t *testing.T) {
	SetJSON(true)
	t.Cleanup(func() { SetJSON(false); warnings = nil })
	Warn("Backup failed: %v (continuing anyway)", "disk full")
	resp := capture(t, func() { Success("deploy", nil) })
	if len(resp.Warnings) != 1 || resp.Warnings[0] != "Backup failed: disk full (continuing anyway)" {
		t.Fatalf("warnings = %q", resp.Warnings)
	}
}
