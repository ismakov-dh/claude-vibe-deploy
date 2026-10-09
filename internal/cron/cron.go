package cron

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/vibe-deploy/vd/internal/shell"
	"github.com/vibe-deploy/vd/internal/state"
)

// Job represents a vd-managed cron entry.
type Job struct {
	App      string `json:"app"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

const tagPrefix = "# vd-cron-"

var (
	scheduleField = regexp.MustCompile(`^[0-9A-Za-z*,/-]+$`)
	scheduleMacro = regexp.MustCompile(`^@(yearly|annually|monthly|weekly|daily|midnight|hourly)$`)
	appName       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`) // same rule as vd deploy's names
)

// Entry builds the crontab line. The host shell runs it, so nothing from the
// caller reaches it unquoted: the schedule is checked field by field, and the
// command is split into words, each quoted, run in the container without a
// shell (as before: wrap it in sh -c yourself for pipes or &&). % is cron's
// newline, so it is escaped.
func Entry(app, schedule, command string) (string, error) {
	if !appName.MatchString(app) {
		return "", fmt.Errorf("invalid app name %q", app)
	}
	fields := strings.Fields(schedule)
	switch {
	case len(fields) == 1 && scheduleMacro.MatchString(fields[0]):
	case len(fields) == 5:
		for _, f := range fields {
			if !scheduleField.MatchString(f) {
				return "", fmt.Errorf("invalid schedule field %q", f)
			}
		}
	default:
		return "", fmt.Errorf("schedule must be 5 fields or @hourly/@daily/…, got %q", schedule)
	}
	for _, r := range command {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("command must be one line of printable text")
		}
	}
	words, err := shell.Split(command)
	if err != nil || len(words) == 0 {
		return "", fmt.Errorf("invalid command %q: %v", command, err)
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		if strings.Contains(w, `\%`) {
			// cron would read the doubled backslash as an escaped backslash
			// and the % as a line break: a job that fails on every run.
			return "", fmt.Errorf(`write %% without a backslash in front of it`)
		}
		quoted[i] = strings.ReplaceAll(shell.Quote(w), "%", `\%`)
	}
	return fmt.Sprintf("%s docker exec %s %s >> %s 2>&1 %s",
		strings.Join(fields, " "), "vd-"+app, strings.Join(quoted, " "),
		state.AppLogsDir(app)+"/cron.log", tagPrefix+app), nil
}

// Set adds or updates the app's cron job: one crontab write, so a crontab
// that cron rejects leaves the previous job in place.
func Set(app, schedule, command string) error {
	entry, err := Entry(app, schedule, command)
	if err != nil {
		return err
	}
	current, _ := shell.RunSimple("crontab", "-l")
	return writeCrontab(append(withoutJob(current, app), entry))
}

// Remove deletes the app's cron job.
func Remove(app string) error {
	if !appName.MatchString(app) {
		return fmt.Errorf("invalid app name %q", app)
	}
	current, err := shell.RunSimple("crontab", "-l")
	if err != nil {
		return nil // no crontab
	}
	return writeCrontab(withoutJob(current, app))
}

// withoutJob is the crontab's lines minus the app's own: the tag must end the
// line, so "ops" does not match ops-dash and a command's text does not match.
func withoutJob(crontab, app string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(crontab, "\n"), "\n") {
		if l != "" && !strings.HasSuffix(strings.TrimRight(l, " "), " "+tagPrefix+app) {
			lines = append(lines, l)
		}
	}
	return lines
}

// writeCrontab replaces the crontab through stdin: no shell between vd and it.
func writeCrontab(lines []string) error {
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	c := exec.Command("crontab", "-")
	c.Stdin = strings.NewReader(body)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab: %v: %s", err, out)
	}
	return nil
}

// List returns all vd-managed cron jobs, optionally filtered by app.
func List(filterApp string) ([]Job, error) {
	current, err := shell.RunSimple("crontab", "-l")
	if err != nil {
		return nil, nil
	}

	return jobsIn(current, filterApp), nil
}

// jobsIn parses vd's own lines out of a crontab.
func jobsIn(current, filterApp string) []Job {
	var jobs []Job
	for _, line := range strings.Split(current, "\n") {
		i := strings.LastIndex(line, " "+tagPrefix)
		if i < 0 {
			continue
		}
		app := strings.TrimSpace(line[i+1+len(tagPrefix):])
		if !appName.MatchString(app) || (filterApp != "" && app != filterApp) {
			continue
		}
		head, rest, ok := strings.Cut(line[:i], " docker exec vd-"+app+" ")
		if !ok {
			continue
		}
		// The command sits between the container and the last " >> ".
		if k := strings.LastIndex(rest, " >> "); k >= 0 {
			rest = rest[:k]
		}
		command := rest
		if words, err := shell.Split(strings.ReplaceAll(rest, `\%`, "%")); err == nil {
			command = strings.Join(words, " ")
		}
		jobs = append(jobs, Job{App: app, Schedule: strings.Join(strings.Fields(head), " "), Command: command})
	}
	return jobs
}
