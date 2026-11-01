package settings

import (
	"io"
	"os"
	"path/filepath"
)

// fileSystem is the only way the store touches disk after the lock is taken. It exists so tests
// can fail a single step (write, fsync, rename) and prove the old file survives.
type fileSystem interface {
	ReadFile(name string) ([]byte, error)
	CreateTemp(dir, pattern string) (tempFile, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	// SyncDir makes renames and removals in dir durable.
	SyncDir(dir string) error
}

type tempFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}

type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// CreateTemp creates the file with mode 0600: the store holds encrypted secrets.
func (osFS) CreateTemp(dir, pattern string) (tempFile, error) { return os.CreateTemp(dir, pattern) }
func (osFS) Rename(oldpath, newpath string) error             { return os.Rename(oldpath, newpath) }
func (osFS) Remove(name string) error                         { return os.Remove(name) }

func (osFS) SyncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}
