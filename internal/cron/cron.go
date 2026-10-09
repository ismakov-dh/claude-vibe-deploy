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
	if strings.ContainsAny(command, "\n\r") {
		return "", fmt.Errorf("command must be one line")
	}
	words, err := shell.Split(command)
	if err != nil || len(words) == 0 {
		return "", fmt.Errorf("invalid command %q: %v", command, err)
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = strings.ReplaceAll(shell.Quote(w), "%", `\%`)
	}
	return fmt.Sprintf("%s docker exec %s %s >> %s 2>&1 %s",
		strings.Join(fields, " "), "vd-"+app, strings.Join(quoted, " "),
		state.AppLogsDir(app)+"/cron.log", tagPrefix+app), nil
}

// Set adds or updates a cron job for an app.
func Set(appName, schedule, command string) error {
	entry, err := Entry(appName, schedule, command)
	if err != nil {
		return err
	}
	// Remove existing entry for this app first
	Remove(appName)
	current, _ := shell.RunSimple("crontab", "-l")
	return writeCrontab(strings.TrimRight(current, "\n") + "\n" + entry + "\n")
}

// Remove deletes all cron jobs for an app.
func Remove(appName string) error {
	tag := tagPrefix + appName
	current, err := shell.RunSimple("crontab", "-l")
	if err != nil {
		return nil // no crontab
	}

	var lines []string
	for _, line := range strings.Split(current, "\n") {
		if !strings.Contains(line, tag) {
			lines = append(lines, line)
		}
	}
	return writeCrontab(strings.Join(lines, "\n"))
}

// writeCrontab replaces the crontab through stdin: no shell between vd and it.
func writeCrontab(body string) error {
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

	var jobs []Job
	for _, line := range strings.Split(current, "\n") {
		if !strings.Contains(line, tagPrefix) {
			continue
		}
		// Extract app name from tag
		idx := strings.Index(line, tagPrefix)
		if idx < 0 {
			continue
		}
		app := strings.TrimSpace(line[idx+len(tagPrefix):])
		if filterApp != "" && app != filterApp {
			continue
		}

		parts := strings.Fields(line)
		n := 5
		if len(parts) > 0 && strings.HasPrefix(parts[0], "@") {
			n = 1
		}
		if len(parts) < n+3 {
			continue
		}
		schedule := strings.Join(parts[:n], " ")
		// The command sits between "docker exec <container> " and " >> ".
		rest := strings.Join(parts[n+3:], " ")
		if k := strings.Index(rest, " >> "); k >= 0 {
			rest = rest[:k]
		}
		command := rest
		if words, err := shell.Split(strings.ReplaceAll(rest, `\%`, "%")); err == nil {
			command = strings.Join(words, " ")
		}

		jobs = append(jobs, Job{
			App:      app,
			Schedule: schedule,
			Command:  command,
		})
	}
	return jobs, nil
}
