package shell

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	for in, want := range map[string][]string{
		`vd cron-set app --schedule "0 2,14 * * *" --command "echo hi"`: {"vd", "cron-set", "app", "--schedule", "0 2,14 * * *", "--command", "echo hi"},
		`a 'b c' d\ e "f\"g" '$(x)' "$(y)" ''`:                          {"a", "b c", "d e", `f"g`, "$(x)", "$(y)", ""},
		"  vd\tstatus  x ":                                              {"vd", "status", "x"},
		`'it'\''s'`:                                                     {"it's"},
	} {
		got, err := Split(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Split(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`a "b`, `a 'b`, `a\`} {
		if _, err := Split(bad); err == nil {
			t.Errorf("Split(%q) accepted an unterminated quote", bad)
		}
	}
	// Quote round-trips through Split as one word, whatever it holds.
	for _, s := range []string{"x; rm -rf /", "it's", "$(id) `id` %", ""} {
		if got, _ := Split(Quote(s)); len(got) != 1 || got[0] != s {
			t.Errorf("Quote(%q) did not round-trip: %q", s, got)
		}
	}
}
