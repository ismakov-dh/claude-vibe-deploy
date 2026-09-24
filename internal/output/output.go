package output

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

var jsonMode bool

// warnings collects Warn calls in --json mode. Every agent runs vd with --json,
// and Warn used to print nothing there — so "DB provisioning failed, deploying
// without DB" or "Backup failed (continuing anyway)" reached no one while the
// response said ok: true. They now ride along in the response, success or
// failure, from whichever command raised them.
var warnings []string

func addWarning(w string) {
	if !slices.Contains(warnings, w) {
		warnings = append(warnings, w)
	}
}

func SetJSON(v bool) { jsonMode = v }
func IsJSON() bool   { return jsonMode }

// Colors
const (
	red    = "\033[0;31m"
	green  = "\033[0;32m"
	yellow = "\033[1;33m"
	reset  = "\033[0m"
)

func Info(msg string, args ...any) {
	if jsonMode {
		return
	}
	fmt.Fprintf(os.Stderr, green+"[vd]"+reset+" "+msg+"\n", args...)
}

func Warn(msg string, args ...any) {
	w := fmt.Sprintf(msg, args...)
	if jsonMode {
		addWarning(w)
		return
	}
	fmt.Fprintf(os.Stderr, yellow+"[vd warning]"+reset+" %s\n", w)
}

func Error(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, red+"[vd error]"+reset+" "+msg+"\n", args...)
}

// VDError is a structured error for JSON output.
type VDError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details string `json:"details,omitempty"`
}

func (e *VDError) Error() string {
	return e.Message
}

func NewError(code, message, hint string) *VDError {
	return &VDError{Code: code, Message: message, Hint: hint}
}

// Response is the top-level JSON output envelope.
type Response struct {
	OK       bool     `json:"ok"`
	Command  string   `json:"command"`
	Data     any      `json:"data,omitempty"`
	Error    *VDError `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Success prints a success response.
func Success(command string, data any) {
	if jsonMode {
		r := Response{OK: true, Command: command, Data: data, Warnings: warnings}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
	}
}

// SuccessWithWarnings prints a success response with warnings, merged with
// anything Warn collected (without repeats — callers often do both).
func SuccessWithWarnings(command string, data any, extra []string) {
	for _, w := range extra {
		addWarning(w)
	}
	if jsonMode {
		r := Response{OK: true, Command: command, Data: data, Warnings: warnings}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
	}
}

// Fail prints an error response and exits.
func Fail(command string, err *VDError) {
	if jsonMode {
		r := Response{OK: false, Command: command, Error: err, Warnings: warnings}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
		os.Exit(1)
	}
	Error("%s", err.Message)
	if err.Hint != "" {
		fmt.Fprintf(os.Stderr, "  hint: %s\n", err.Hint)
	}
	if err.Details != "" {
		fmt.Fprintf(os.Stderr, "  details: %s\n", err.Details)
	}
	os.Exit(1)
}
