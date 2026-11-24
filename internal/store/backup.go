package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Backup takes a snapshot of the source at its current schema version.
func Backup(ctx context.Context, source, target string) error {
	s, _, err := openInitialized(ctx, source, ReadOnly)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Backup(ctx, target)
}

func (s *Store) Backup(ctx context.Context, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("backup destination %q already exists", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(target)
	work, err := os.MkdirTemp(dir, ".hostdb-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	db := filepath.Join(work, "backup.db")
	f, err := os.OpenFile(db, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", db); err != nil {
		return fmt.Errorf("SQLite backup: %w", err)
	}
	f, err = os.Open(db)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Publish a complete backup without overwriting an existing destination.
	if err := os.Link(db, target); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
