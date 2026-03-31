package cron

import (
	"fmt"
	"strings"
	"time"

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

// Set adds or updates a cron job for an app.
func Set(appName, schedule, command string) error {
	// Remove existing entry for this app first
	Remove(appName)

	logPath := state.AppLogsDir(appName) + "/cron.log"
	containerName := "vd-" + appName
	tag := tagPrefix + appName

	entry := fmt.Sprintf("%s docker exec %s %s >> %s 2>&1 %s",
		schedule, containerName, command, logPath, tag)

	// Read current crontab
	current, _ := shell.RunSimple("crontab", "-l")
	newCrontab := strings.TrimRight(current, "\n") + "\n" + entry + "\n"

	// Write new crontab via stdin
	_, err := shell.Run(30*time.Second, "sh", "-c", fmt.Sprintf("echo %q | crontab -", newCrontab))
	return err
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
	newCrontab := strings.Join(lines, "\n")
	_, err = shell.Run(30*time.Second, "sh", "-c", fmt.Sprintf("echo %q | crontab -", newCrontab))
	return err
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

		// Parse: schedule(5 fields) docker exec <container> <cmd> >> <log> tag
		parts := strings.Fields(line)
		if len(parts) < 8 {
			continue
		}
		schedule := strings.Join(parts[:5], " ")
		// Find command between container name and ">>"
		cmdParts := []string{}
		for i := 8; i < len(parts); i++ { // skip: 5 schedule + docker + exec + container
			if parts[i] == ">>" {
				break
			}
			cmdParts = append(cmdParts, parts[i])
		}

		jobs = append(jobs, Job{
			App:      app,
			Schedule: schedule,
			Command:  strings.Join(cmdParts, " "),
		})
	}
	return jobs, nil
}
