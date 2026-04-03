package cmd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/vibe-deploy/vd/internal/output"
	"github.com/vibe-deploy/vd/internal/state"
)

func init() {
	rootCmd.AddCommand(pushCmd)
}

var pushCmd = &cobra.Command{
	Use:   "push <app-name>",
	Short: "Receive app files via stdin (tar stream)",
	Long:  "Usage: tar cf - --exclude='node_modules' --exclude='.git' ./app | ssh vd-server \"vd push myapp\"",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]

		destDir := "/tmp/vd-push-" + name
		os.RemoveAll(destDir)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to create directory: "+err.Error(), ""))
		}

		// Extract tar from stdin
		tar := exec.Command("tar", "xf", "-", "-C", destDir)
		tar.Stdin = io.Reader(os.Stdin)
		tar.Stderr = os.Stderr
		if err := tar.Run(); err != nil {
			os.RemoveAll(destDir)
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to extract tar: "+err.Error(), "Pipe a tar stream: tar cf - --exclude='node_modules' --exclude='.git' ./app | ssh vd-server \"vd push myapp\""))
		}

		// Check if tar extracted into a subdirectory (tar cf - ./app creates app/ inside)
		// Filter out macOS resource forks (._*) and hidden files
		entries, _ := os.ReadDir(destDir)
		var dirs []os.DirEntry
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirs = append(dirs, e)
			}
		}
		srcDir := destDir
		if len(dirs) == 1 {
			srcDir = destDir + "/" + dirs[0].Name()
		}

		// Move to the standard push location
		finalDir := state.PushDir(name)
		os.MkdirAll(filepath.Dir(finalDir), 0755)
		os.RemoveAll(finalDir)
		os.Rename(srcDir, finalDir)
		if srcDir != destDir {
			os.RemoveAll(destDir)
		}

		output.Info("Received files for %s at %s", name, finalDir)
		output.Success("push", map[string]any{
			"name": name,
			"path": finalDir,
		})
	},
}
