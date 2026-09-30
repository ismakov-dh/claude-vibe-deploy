package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// ChownLikeHome gives path the owner of VD_HOME. vd runs both as root (an admin
// over a login shell) and as vd-user (every agent, via the ssh wrapper); files a
// root run creates would otherwise be unusable to the next agent. Fails with
// EPERM when not root, which is exactly when ownership is already right.
func ChownLikeHome(path string) {
	if fi, err := os.Stat(VDHome()); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			os.Chown(path, int(st.Uid), int(st.Gid))
		}
	}
}

// LockAuthentik serialises vd's writes to Authentik across processes.
//
// The embedded outpost's providers list is one shared object, updated by
// read-modify-write. Two concurrent `vd deploy --auth` runs would each read the
// same list, add their own provider and write it back — and the second write
// would drop the first app's provider, unpublishing it with no error anywhere.
// flock is released by the kernel if vd dies, so a crashed deploy cannot wedge
// the next one.
//
// ponytail: one global lock; per-app locking is pointless here since every run
// touches the same outpost object.
func LockAuthentik() (unlock func(), err error) {
	return lockFile(filepath.Join(VDHome(), "authentik.lock"))
}

// LockApp serialises everything that rewrites one app's compose file and
// containers — vd deploy and vd mcp-oauth. Without it a deploy that adds
// forward-auth labels and an mcp-oauth that re-renders the file from the older
// manifest could interleave, and the next full `compose up` would publish the
// app without its login. Separate from the Authentik lock, which both take
// inside: flock on a second open of the same file would block its own process.
func LockApp(app string) (unlock func(), err error) {
	dir := filepath.Join(VDHome(), "locks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	ChownLikeHome(dir)
	return lockFile(filepath.Join(dir, app+".lock"))
}

// held keeps every locked file reachable until its unlock. The flock lives as
// long as the descriptor, and an *os.File nobody references is closed by its
// finalizer — a caller that dropped unlock would lose the lock at the next GC,
// say halfway through a docker build.
var (
	heldMu sync.Mutex
	held   = map[*os.File]struct{}{}
)

func lockFile(path string) (unlock func(), err error) {
	// Read-only is enough for flock, and it keeps working when the file was
	// created by a root-run vd and is not writable by vd-user.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	ChownLikeHome(path)
	heldMu.Lock()
	held[f] = struct{}{}
	heldMu.Unlock()
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		heldMu.Lock()
		delete(held, f)
		heldMu.Unlock()
	}, nil
}
