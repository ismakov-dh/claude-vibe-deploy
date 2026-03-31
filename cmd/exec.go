package cmd

import (
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

func init() {
	rootCmd.AddCommand(execCmd)
}

var execCmd = &cobra.Command{
	Use:                "exec <app-name> -- <command...>",
	Short:              "Run a command inside an app container",
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true,
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		m, err := state.LoadManifest(name)
		if err != nil {
			output.Fail("exec", output.NewError("NOT_FOUND", "App not found: "+name, ""))
		}

		// Find -- separator
		cmdArgs := args[1:]
		for i, a := range cmdArgs {
			if a == "--" {
				cmdArgs = cmdArgs[i+1:]
				break
			}
		}
		if len(cmdArgs) == 0 {
			output.Fail("exec", output.NewError("NO_COMMAND", "No command specified", "Usage: vd exec <app> -- <command>"))
		}

		dockerArgs := append([]string{"exec", "-it", m.ContainerName}, cmdArgs...)
		c := exec.Command("docker", dockerArgs...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Run()
	},
}
