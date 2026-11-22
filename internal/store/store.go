package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

const applicationID = 0x48444231

type Mode int

const (
	ReadOnly Mode = iota
	Write
	Initialize
)

type runner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Queries struct {
	db runner
}

type Store struct {
	Queries
	db *sql.DB
}

func Path(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv("HOSTDB_DB")
	}
	if explicit == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if !filepath.IsAbs(base) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
		explicit = filepath.Join(base, "hostdb", "hosts.db")
	}
	return filepath.Abs(explicit)
}

func Open(ctx context.Context, path string, mode Mode) (*Store, error) {
	if mode == Initialize {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		switch {
		case err == nil:
			if err := f.Close(); err != nil {
				return nil, err
			}
		case !errors.Is(err, os.ErrExist):
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("database %q does not exist; run hostdb init", path)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database %q must be a regular file", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("database %q is accessible to other users; run chmod 600 on it", path)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, fmt.Errorf("unsafe SQLite sidecar %q", path+suffix)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(ON)"}}
	if mode != ReadOnly {
		q.Set("mode", "rw")
		q.Set("_txlock", "immediate")
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{Queries: Queries{db}, db: db}
	if err := s.prepare(ctx, mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	return s, nil
}

func (s *Store) prepare(ctx context.Context, mode Mode) error {
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	version, err := schemaVersion(ctx, s.db, len(files))
	if err != nil {
		return err
	}
	if mode == ReadOnly {
		if version < len(files) {
			return errors.New("schema upgrade required; run hostdb init")
		}
		return nil
	}
	var journal string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return err
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("could not enable WAL journal mode (got %s)", journal)
	}
	if version == len(files) {
		return nil
	}
	return s.Transaction(ctx, func(q Queries) error {
		// Another process may have migrated the database while we waited for the lock.
		version, err := schemaVersion(ctx, q.db, len(files))
		if err != nil {
			return err
		}
		if version == 0 {
			var tables int
			err := q.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables)
			if err != nil {
				return err
			}
			if tables != 0 {
				return errors.New("unrecognized SQLite database")
			}
			if _, err := q.db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", applicationID)); err != nil {
				return err
			}
		}
		for i, file := range files[version:] {
			migration, err := migrations.ReadFile("migrations/" + file.Name())
			if err != nil {
				return err
			}
			if _, err := q.db.ExecContext(ctx, string(migration)); err != nil {
				return fmt.Errorf("migration %s: %w", file.Name(), err)
			}
			if _, err := q.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", version+i+1)); err != nil {
				return err
			}
		}
		return nil
	})
}

func schemaVersion(ctx context.Context, db runner, latest int) (int, error) {
	var version, appID int
	err := db.QueryRowContext(ctx, "SELECT user_version, application_id FROM pragma_user_version(), pragma_application_id()").Scan(&version, &appID)
	if err != nil {
		return 0, err
	}
	if (version != 0 || appID != 0) && appID != applicationID {
		return 0, errors.New("SQLite file is not a hostdb database")
	}
	if version < 0 || version > latest {
		return 0, fmt.Errorf("unsupported database schema %d (this hostdb supports through %d)", version, latest)
	}
	return version, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Transaction(ctx context.Context, fn func(Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(Queries{tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (q Queries) SyncPath(ctx context.Context) (string, error) {
	var value string
	err := q.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='ssh_sync_path'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (q Queries) SetSyncPath(ctx context.Context, path string) error {
	_, err := q.db.ExecContext(ctx, "INSERT INTO settings VALUES ('ssh_sync_path', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", path)
	return err
}

func collect[T any](ctx context.Context, db runner, query string, scan func(*sql.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []T{}
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func changed(result sql.Result, err error, label string) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("%s not found", label)
	}
	return nil
}
