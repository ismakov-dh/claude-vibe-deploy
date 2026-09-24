package state

import (
	"fmt"
	"os"
	"path/filepath"
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
	path := filepath.Join(VDHome(), "authentik.lock")
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
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
