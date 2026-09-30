package state

import (
	"runtime"
	"testing"
	"time"
)

// The lock must actually exclude: a second holder waits until the first lets go.
func TestLockAuthentikExcludes(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	unlock, err := LockAuthentik()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u, err := LockAuthentik()
		if err != nil {
			t.Error(err)
			return
		}
		close(got)
		u()
	}()
	select {
	case <-got:
		t.Fatal("second lock acquired while the first was held")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after release")
	}
}

// A caller that drops unlock must still hold the lock after a GC.
func TestLockSurvivesGCWithoutUnlockReference(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	func() {
		if _, err := LockApp("gc"); err != nil {
			t.Fatal(err)
		}
	}()
	for i := 0; i < 5; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	got := make(chan struct{})
	go func() {
		u, _ := LockApp("gc")
		u()
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("the lock was released by the garbage collector")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestLockAppIsPerAppAndIndependentOfAuthentik(t *testing.T) {
	t.Setenv("VD_HOME", t.TempDir())
	a, err := LockApp("a")
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	// Other app and the Authentik lock stay free while "a" is held.
	b, err := LockApp("b")
	if err != nil {
		t.Fatal(err)
	}
	b()
	au, err := LockAuthentik()
	if err != nil {
		t.Fatal(err)
	}
	au()
	got := make(chan struct{})
	go func() {
		u, _ := LockApp("a")
		u()
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second holder of app a's lock did not wait")
	case <-time.After(200 * time.Millisecond):
	}
}
