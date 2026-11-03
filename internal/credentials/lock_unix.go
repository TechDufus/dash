//go:build unix

package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock takes an exclusive flock on dir/.credentials.lock. The lock file is separate from
// credentials.yaml and never replaced: the file is renamed over on every write, and a lock on it
// would follow the old inode. The lock is released when the returned file is closed (or the
// process dies), so a crash never leaves a stale lock. flock locks belong to the open file
// description, so a second Open in the same process is refused as well.
func acquireLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, LockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("credentials: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: data directory %s is in use (lock held on %s); run one dash instance per data directory", ErrLocked, dir, path)
		}
		return nil, fmt.Errorf("credentials: lock %s: %w", path, err)
	}
	return f, nil
}
