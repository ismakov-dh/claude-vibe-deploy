package main

import (
	"os"

	"github.com/vibe-deploy/vd/cmd"
)

var Version = "dev"

func main() {
	cmd.SetVersion(Version)
	cmd.SetTemplatesFS(templatesFS)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
