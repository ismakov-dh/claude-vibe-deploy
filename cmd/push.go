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

		// Everything lands in the push root vd-user owns, under names nobody can
		// predict: not /tmp, where another local user could plant the path first.
		finalDir := state.PushDir(name)
		root := filepath.Dir(finalDir)
		if err := os.MkdirAll(root, 0755); err != nil {
			output.Fail("push", output.NewError("PUSH_FAILED",
				"Failed to create push directory: "+err.Error(),
				"Ensure the vd-user owns "+root+" and that the SSH session runs as vd-user via the agent key."))
		}
		archive, destDir := "", ""
		// output.Fail exits: deferred cleanup would never run.
		fail := func(e *output.VDError) {
			if archive != "" {
				os.Remove(archive)
			}
			if destDir != "" {
				os.RemoveAll(destDir)
			}
			output.Fail("push", e)
		}

		// The stream goes to a file first and is checked before tar writes
		// anything: extracting through a link writes outside the push.
		f, err := os.CreateTemp(root, ".incoming-"+name+"-*.tar")
		if err != nil {
			fail(output.NewError("PUSH_FAILED", "Failed to receive the tar stream: "+err.Error(), ""))
		}
		archive = f.Name()
		if err := saveStream(f, os.Stdin); err != nil {
			fail(output.NewError("PUSH_FAILED", "Failed to receive the tar stream: "+err.Error(), ""))
		}
		if err := checkTar(archive); err != nil {
			fail(output.NewError("INVALID_SOURCE", err.Error(), sourceHint))
		}

		if destDir, err = os.MkdirTemp(root, ".incoming-"+name+"-"); err != nil {
			fail(output.NewError("PUSH_FAILED", "Failed to create directory: "+err.Error(), ""))
		}
		os.Chmod(destDir, 0755) // MkdirTemp makes 0700; the push dir was always 0755
		untar := exec.Command("tar", "xf", archive, "-C", destDir)
		untar.Stderr = os.Stderr
		if err := untar.Run(); err != nil {
			fail(output.NewError("PUSH_FAILED",
				"Failed to extract tar: "+err.Error(), "Pipe a tar stream: tar cf - --exclude='node_modules' --exclude='.git' ./app | ssh vd-server \"vd push myapp\""))
		}
		os.Remove(archive)
		archive = ""

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

// maxPush bounds a push: the stream is stored, then extracted, on a disk
// every app shares.
const maxPush = 4 << 30

// sourceHint is what to do about a refused entry in a push or a deploy source.
const sourceHint = "Push real files and directories only: replace links with what they point to (tar -h stores the targets) or exclude them, and push again"

func saveStream(f *os.File, r io.Reader) error {
	n, err := io.Copy(f, io.LimitReader(r, maxPush+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxPush {
		err = fmt.Errorf("the push is larger than %d GiB", maxPush>>30)
	}
	return err
}

// checkTar accepts regular files and directories only. A link could put a
// host file in the image or write outside the push; a FIFO would hang the
// deploy that reads it (holding the app lock); devices have no business here.
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
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("%s is a link (to %s): vd does not take links in a push", h.Name, h.Linkname)
		default:
			return fmt.Errorf("%s is not a regular file or directory (tar type %q)", h.Name, h.Typeflag)
		}
	}
}
