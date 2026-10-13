package cmd

import (
	"archive/tar"
	"errors"
	"fmt"
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
		// The name becomes a path that is emptied below: "../apps/x" would wipe another app.
		if !nameRegex.MatchString(name) {
			output.Fail("push", output.NewError("INVALID_NAME", "Invalid app name: "+name,
				"Lowercase letters, digits and hyphens, 2-63 chars, starting with a letter"))
		}

		// The stream goes to a file first: its links are refused before tar
		// writes anything, since extracting through one writes outside the push.
		archive := "/tmp/vd-push-" + name + ".tar"
		defer os.Remove(archive)
		if err := saveStdin(archive); err != nil {
			output.Fail("push", output.NewError("PUSH_FAILED", "Failed to receive the tar stream: "+err.Error(), ""))
		}
		if err := checkTar(archive); err != nil {
			output.Fail("push", output.NewError("INVALID_SOURCE", err.Error(),
				"Push real files: replace links with what they point to (or exclude them) and push again"))
		}

		destDir := "/tmp/vd-push-" + name
		os.RemoveAll(destDir)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to create directory: "+err.Error(), ""))
		}

		// Extract tar from stdin
		untar := exec.Command("tar", "xf", archive, "-C", destDir)
		untar.Stderr = os.Stderr
		if err := untar.Run(); err != nil {
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

		// Move to the standard push location.
		finalDir := state.PushDir(name)
		if err := os.MkdirAll(filepath.Dir(finalDir), 0755); err != nil {
			os.RemoveAll(destDir)
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to create push directory: "+err.Error(),
				"Ensure the vd-user owns "+filepath.Dir(finalDir)+" and that the SSH session runs as vd-user via the agent key."))
		}
		if err := os.RemoveAll(finalDir); err != nil {
			os.RemoveAll(destDir)
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to clear previous push at "+finalDir+": "+err.Error(),
				"The existing directory may be owned by a different user. SSH as vd-user via the agent key."))
		}
		if err := os.Rename(srcDir, finalDir); err != nil {
			os.RemoveAll(destDir)
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to move extracted files to "+finalDir+": "+err.Error(),
				"SSH as vd-user via the agent key — only vd-user can write to "+filepath.Dir(finalDir)+"."))
		}
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

func saveStdin(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, os.Stdin); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// checkTar refuses an archive with a symbolic or hard link in it: vd never
// follows links, so a link to a host file can't end up in an app's image.
func checkTar(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("not a readable tar stream: %w", err)
		}
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			return fmt.Errorf("%s is a link (to %s): vd does not take links in a push", h.Name, h.Linkname)
		}
	}
}
