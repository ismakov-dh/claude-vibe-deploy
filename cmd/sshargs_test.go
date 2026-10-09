package cmd

import (
	"strings"
	"testing"
)

func TestSSHArgs(t *testing.T) {
	args, e := sshArgs(`vd cron-set ops-dash --schedule "0 2,14 * * *" --command 'python refresh.py' --json`)
	if e != nil || strings.Join(args, "|") != "cron-set|ops-dash|--schedule|0 2,14 * * *|--command|python refresh.py|--json" {
		t.Fatalf("args %q err %v", args, e)
	}
	for line, code := range map[string]string{
		"vd exec app -- id":        "FORBIDDEN",
		"vd --json exec app -- id": "FORBIDDEN", // got past the old wrapper's prefix check
		"vd  exec app -- id":       "FORBIDDEN",
		"vd db-backup-all":         "FORBIDDEN",
		"sh -c id":                 "FORBIDDEN",
		"vd status 'x":             "INVALID_ARGS",
	} {
		if _, e := sshArgs(line); e == nil || e.Code != code {
			t.Errorf("%q: got %v, want %s", line, e, code)
		}
	}
}
