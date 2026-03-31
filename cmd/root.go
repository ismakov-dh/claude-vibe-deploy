package cmd

import (
	"io/fs"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/output"
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
	return rootCmd.Execute()
}
