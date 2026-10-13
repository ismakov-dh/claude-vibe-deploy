package cmd

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTar(t *testing.T, hdrs ...*tar.Header) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "push.tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(make([]byte, h.Size))
		}
	}
	tw.Close()
	f.Close()
	return p
}

// A push may hold only real files and directories: a link would let a host
// file (or a write through it) into the app.
func TestCheckTarRefusesLinks(t *testing.T) {
	dir := &tar.Header{Name: "app/", Typeflag: tar.TypeDir, Mode: 0755}
	file := &tar.Header{Name: "app/main.py", Typeflag: tar.TypeReg, Mode: 0644, Size: 3}
	if err := checkTar(writeTar(t, dir, file)); err != nil {
		t.Fatalf("plain tree refused: %v", err)
	}
	for _, link := range []*tar.Header{
		{Name: "app/key", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "app/copy", Typeflag: tar.TypeLink, Linkname: "app/main.py"},
	} {
		err := checkTar(writeTar(t, dir, file, link))
		if err == nil || !strings.Contains(err.Error(), link.Name+" is a link") {
			t.Errorf("%s: %v", link.Name, err)
		}
	}
	// A FIFO would hang the deploy that reads it; devices have no place here.
	for _, odd := range []*tar.Header{
		{Name: "app/pipe", Typeflag: tar.TypeFifo, Mode: 0644},
		{Name: "app/dev", Typeflag: tar.TypeChar, Mode: 0644},
	} {
		err := checkTar(writeTar(t, dir, file, odd))
		if err == nil || !strings.Contains(err.Error(), odd.Name+" is not a regular file") {
			t.Errorf("%s: %v", odd.Name, err)
		}
	}
	if err := checkTar(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("missing archive accepted")
	}
}
