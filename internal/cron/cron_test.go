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

// Only the app's own line goes, and List reads back what Entry wrote.
func TestJobLinesMatchExactly(t *testing.T) {
	t.Setenv("VD_HOME", "/opt/vibe-deploy")
	dash, _ := Entry("ops-dash", "@daily", `sh -c 'echo x >> /tmp/f'`)
	ops, _ := Entry("ops", "0 2,14 * * *", `echo "# vd-cron-victim"`)
	crontab := "MAILTO=x\n" + dash + "\n" + ops + "\n"
	if got := withoutJob(crontab, "ops"); len(got) != 2 || got[1] != dash {
		t.Fatalf("removing ops left %q", got)
	}
	if got := withoutJob(crontab, "victim"); len(got) != 3 {
		t.Fatalf("a command's text matched as a tag: %q", got)
	}
	jobs := jobsIn(crontab, "")
	if len(jobs) != 2 || jobs[0].Command != "sh -c echo x >> /tmp/f" || jobs[0].Schedule != "@daily" ||
		jobs[1].App != "ops" || jobs[1].Schedule != "0 2,14 * * *" || jobs[1].Command != "echo # vd-cron-victim" {
		t.Fatalf("jobs %+v", jobs)
	}
	if Remove("") == nil {
		t.Fatal("an empty app name would match every vd job")
	}
}
