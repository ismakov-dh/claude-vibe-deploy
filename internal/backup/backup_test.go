package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-deploy/vd/internal/state"
)

// fakeDocker records the docker calls Create, Restore and prune make.
type fakeDocker struct {
	calls  []string
	images map[string]bool
}

func stubDocker(t *testing.T) *fakeDocker {
	t.Helper()
	t.Setenv("VD_HOME", t.TempDir())
	f := &fakeDocker{images: map[string]bool{}}
	saved := []any{imageIDOf, imageRefOf, tagImage, untagImage, imageExists, composeDown, composeUp, composeUpNoBuild}
	t.Cleanup(func() {
		imageIDOf, imageRefOf = saved[0].(func(string) (string, error)), saved[1].(func(string) (string, error))
		tagImage, untagImage = saved[2].(func(string, string) error), saved[3].(func(string) error)
		imageExists = saved[4].(func(string) bool)
		composeDown, composeUp, composeUpNoBuild = saved[5].(func(string, string) error), saved[6].(func(string, string) error), saved[7].(func(string, string) error)
	})
	imageIDOf = func(string) (string, error) { return "sha256:old", nil }
	imageRefOf = func(string) (string, error) { return "demo-demo", nil }
	tagImage = func(src, tag string) error {
		f.calls = append(f.calls, "tag "+src+" "+tag)
		f.images[tag] = true
		return nil
	}
	untagImage = func(tag string) error { f.calls = append(f.calls, "untag "+tag); delete(f.images, tag); return nil }
	imageExists = func(ref string) bool { return f.images[ref] }
	composeDown = func(string, string) error { f.calls = append(f.calls, "down"); return nil }
	composeUp = func(string, string) error { f.calls = append(f.calls, "up --build"); return nil }
	composeUpNoBuild = func(string, string) error { f.calls = append(f.calls, "up --no-build"); return nil }
	if err := os.MkdirAll(state.AppDir("demo"), 0755); err != nil {
		t.Fatal(err)
	}
	return f
}

// A backup keeps the running image under a tag of its own; a rollback runs
// exactly that image again, never a build of the current source.
func TestRollbackRunsThePreviousImage(t *testing.T) {
	f := stubDocker(t)
	if err := Create("demo"); err != nil {
		t.Fatal(err)
	}
	_, meta, err := Latest("demo")
	if err != nil || !strings.HasPrefix(meta.BackupImage, "vd-backup-demo:") || meta.ComposeImage != "demo-demo" {
		t.Fatalf("metadata %+v %v", meta, err)
	}
	if !f.images[meta.BackupImage] {
		t.Fatal("running image not kept under the backup tag")
	}

	f.calls = nil
	got, err := Restore("demo", func(*Metadata) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"down", "tag " + meta.BackupImage + " demo-demo", "up --no-build"}
	if strings.Join(f.calls, "; ") != strings.Join(want, "; ") || got.RebuiltFromSource {
		t.Fatalf("calls %v rebuilt %v, want %v", f.calls, got.RebuiltFromSource, want)
	}
}

// A backup whose image tag is gone is refused before anything stops.
func TestRollbackWithMissingImageChangesNothing(t *testing.T) {
	f := stubDocker(t)
	if err := Create("demo"); err != nil {
		t.Fatal(err)
	}
	f.images = map[string]bool{}
	f.calls = nil
	if _, err := Restore("demo", func(*Metadata) error { return nil }); err == nil || len(f.calls) != 0 {
		t.Fatalf("err %v, calls %v", err, f.calls)
	}
}

// Backups from before backup_image have no image: rebuilt from src, and said so.
func TestRollbackOfLegacyBackupRebuildsAndSaysSo(t *testing.T) {
	f := stubDocker(t)
	dir := filepath.Join(state.AppBackupsDir("demo"), "20260101_000000")
	os.MkdirAll(dir, 0755)
	b, _ := json.Marshal(Metadata{App: "demo", Timestamp: "20260101_000000", ImageID: "sha256:x"})
	os.WriteFile(filepath.Join(dir, "metadata.json"), b, 0644)
	os.WriteFile(filepath.Join(dir, "image.tar.gz"), []byte{0x1f, 0x8b}, 0644) // the empty archives on prod

	got, err := Restore("demo", func(*Metadata) error { return nil })
	if err != nil || !got.RebuiltFromSource || strings.Join(f.calls, "; ") != "down; up --build" {
		t.Fatalf("err %v rebuilt %v calls %v", err, got != nil && got.RebuiltFromSource, f.calls)
	}
}

// Pruning a backup drops its image tag too, or every deploy would keep one more image.
func TestPruneDropsTheImageTag(t *testing.T) {
	f := stubDocker(t)
	for i := 0; i <= maxBackups; i++ {
		dir := filepath.Join(state.AppBackupsDir("demo"), "2026010"+string(rune('0'+i))+"_000000")
		os.MkdirAll(dir, 0755)
		b, _ := json.Marshal(Metadata{App: "demo", BackupImage: "vd-backup-demo:" + string(rune('0'+i))})
		os.WriteFile(filepath.Join(dir, "metadata.json"), b, 0644)
	}
	prune("demo")
	if strings.Join(f.calls, "; ") != "untag vd-backup-demo:0" {
		t.Fatalf("calls %v", f.calls)
	}
}

// A backup that fails leaves nothing behind: Latest must still find the
// previous, complete one (the auto-rollback of this deploy depends on it).
func TestFailedBackupLeavesNoDirectory(t *testing.T) {
	f := stubDocker(t)
	if err := Create("demo"); err != nil {
		t.Fatal(err)
	}
	_, good, _ := Latest("demo")
	tagImage = func(string, string) error { return os.ErrPermission }
	time.Sleep(1100 * time.Millisecond) // a new timestamp directory
	if err := Create("demo"); err == nil {
		t.Fatal("failed tag not reported")
	}
	if _, meta, err := Latest("demo"); err != nil || meta.Timestamp != good.Timestamp {
		t.Fatalf("Latest after a failed backup: %+v %v", meta, err)
	}
	_ = f
}
