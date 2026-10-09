package cmd

import (
	"io/fs"
	"os"
	"slices"

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
	if line := os.Getenv("SSH_ORIGINAL_COMMAND"); len(args) == 0 && line != "" {
		var e *output.VDError
		if args, e = sshArgs(line); e != nil {
			output.SetJSON(true)
			output.Fail("ssh", e)
		}
		rootCmd.SetArgs(args)
	}
	err := rootCmd.Execute()
	if err != nil {
		// Parse errors (unknown flag, wrong arg count) never reach a command's
		// Run; SilenceErrors would otherwise make them a bare exit 1.
		output.SetJSON(slices.Contains(args, "--json"))
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
	args := words[1:]
	if c, _, err := rootCmd.Find(args); err == nil {
		switch c.Name() {
		case "exec":
			return nil, output.NewError("FORBIDDEN", "vd exec is disabled", "")
		case "db-backup-all":
			return nil, output.NewError("FORBIDDEN", "vd db-backup-all is disabled via SSH", "")
		}
	}
	return args, nil
}
