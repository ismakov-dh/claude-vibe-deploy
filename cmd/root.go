package cmd

import (
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/shell"
)

var (
	version     string
	templatesFS fs.FS
	jsonFlag    bool
)

func SetVersion(v string)    { version = v }
func SetTemplatesFS(f fs.FS) { templatesFS = f }
func GetTemplatesFS() fs.FS  { return templatesFS }

var rootCmd = &cobra.Command{
	Use:   "vd",
	Short: "vibe-deploy — deploy vibecoded apps to bare metal",
	Long:  "A deployment CLI for non-programmers. Designed to be called by AI agents.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		output.SetJSON(jsonFlag)
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonFlag, "json", false, "output structured JSON")
}

func Execute() error {
	args := os.Args[1:]
	// Called by vd-ssh-wrapper (the forced command) with no arguments: the
	// command line comes from SSH_ORIGINAL_COMMAND, split here with sh quoting
	// and no expansion — bash word-splitting cut "0 2 * * *" apart and globbed it.
	if line := os.Getenv("SSH_ORIGINAL_COMMAND"); line != "" {
		var e *output.VDError
		if len(args) == 0 {
			args, e = sshArgs(line)
			rootCmd.SetArgs(args)
		} else {
			e = forbiddenOverSSH(args) // an older wrapper that still passes words
		}
		if e != nil {
			output.SetJSON(true)
			output.Fail("ssh", e)
		}
	}
	err := rootCmd.Execute()
	if err != nil {
		// Parse errors (unknown flag, wrong arg count) never reach a command's
		// Run; SilenceErrors would otherwise make them a bare exit 1.
		output.SetJSON(jsonFlag || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--json") }))
		output.Fail("vd", output.NewError("INVALID_ARGS", err.Error(), "See: vd <command> --help"))
	}
	return err
}

// sshArgs turns an SSH command line into vd's arguments, refusing what agents
// may not run. The check is on the command cobra resolves, so flags before it
// ("vd --json exec …") do not get past.
func sshArgs(line string) ([]string, *output.VDError) {
	words, err := shell.Split(line)
	if err != nil {
		return nil, output.NewError("INVALID_ARGS", "Cannot parse the command: "+err.Error(), "Quote with ' or \"")
	}
	if len(words) == 0 || words[0] != "vd" {
		return nil, output.NewError("FORBIDDEN", "Only vd commands are allowed", "")
	}
	return words[1:], forbiddenOverSSH(words[1:])
}

func forbiddenOverSSH(args []string) *output.VDError {
	if c, _, err := rootCmd.Find(args); err == nil {
		switch c.Name() {
		case "exec":
			return output.NewError("FORBIDDEN", "vd exec is disabled", "")
		case "db-backup-all":
			return output.NewError("FORBIDDEN", "vd db-backup-all is disabled via SSH", "")
		case "mcp-oauth":
			for _, a := range args {
				if strings.HasPrefix(a, "--drop-strict") {
					return output.NewError("FORBIDDEN", "vd mcp-oauth --drop-strict is for platform admins, not via SSH", "")
				}
			}
		}
	}
	return nil
}
