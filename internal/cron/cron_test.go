package cron

import (
	"strings"
	"testing"

	"github.com/vibe-deploy/vd/internal/shell"
)

func TestEntryQuotesEverything(t *testing.T) {
	t.Setenv("VD_HOME", "/opt/vibe-deploy")
	e, err := Entry("ops-dash", "0 2,14 * * *", `python refresh.py --note "a b; $(id) 100%"`)
	if err != nil {
		t.Fatal(err)
	}
	want := `0 2,14 * * * docker exec vd-ops-dash 'python' 'refresh.py' '--note' 'a b; $(id) 100\%' >> /opt/vibe-deploy/logs/ops-dash/cron.log 2>&1 # vd-cron-ops-dash`
	if e != want {
		t.Fatalf("entry\n got %s\nwant %s", e, want)
	}
	// What the host shell gets after cron's % handling: the same four words.
	cmd := strings.SplitN(strings.ReplaceAll(e, `\%`, "%"), " >> ", 2)[0]
	words, _ := shell.Split(cmd)
	if got := words[8:]; strings.Join(got, "|") != "python|refresh.py|--note|a b; $(id) 100%" {
		t.Fatalf("host shell sees %q", got)
	}
	if _, err := Entry("xx", "@hourly", "true"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{
		{"* * * *", "true"},              // 4 fields
		{"* * * * *\n* * * * *", "true"}, // smuggled second line
		{"* * * * $(id)", "true"},
		{"@reboot", "true"},
		{"* * * * *", "true\nid"},
		{"* * * * *", `echo "x`},
		{"* * * * *", "  "},
	} {
		if _, err := Entry("xx", bad[0], bad[1]); err == nil {
			t.Errorf("accepted schedule %q command %q", bad[0], bad[1])
		}
	}
	if _, err := Entry("x;id", "@hourly", "true"); err == nil {
		t.Error("accepted an app name that is not a vd name")
	}
}
