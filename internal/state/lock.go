package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
