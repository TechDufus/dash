//go:build unix

package audit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockName is the audit log's own lock file. It is not the settings store's ".lock", which the
// store holds for its lifetime, and it is never renamed, so a lock on it cannot follow a
// replaced inode.
const lockName = ".audit.lock"

// acquireLock takes an exclusive flock on dir/.audit.lock. Two writers on one log would
// interleave lines and fork the hash chain, so a second dash on the same data dir is refused,
// whether or not it can write settings. The lock is released when the returned file is closed
// or the process dies.
func acquireLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: data directory %s is in use (lock held on %s); run one dash instance per data directory", ErrLocked, dir, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}
