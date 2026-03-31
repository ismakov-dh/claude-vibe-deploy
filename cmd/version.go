package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/output"
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print vibe-deploy version",
	Run: func(cmd *cobra.Command, args []string) {
		if output.IsJSON() {
			output.Success("version", map[string]string{"version": version})
			return
		}
		fmt.Printf("vd %s\n", version)
	},
}
