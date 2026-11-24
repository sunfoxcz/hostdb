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
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrations embed.FS

var migrationFiles, migrationFilesErr = migrations.ReadDir("migrations")

const applicationID = 0x48444231

type Mode int

const (
	// ReadOnly returns a read-only connection after any pending migrations.
	ReadOnly Mode = iota
	Write
)

type InitResult int

const (
	Initialized InitResult = iota
	AlreadyInitialized
	UpgradeRequired
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
	s, version, err := openInitialized(ctx, path, mode)
	if err != nil {
		return nil, err
	}
	if mode == ReadOnly {
		if version == len(migrationFiles) {
			return s, nil
		}
		s.Close()
		if err := upgrade(ctx, path); err != nil {
			return nil, fmt.Errorf("automatically upgrade database %q: %w", path, err)
		}
		s, version, err = openInitialized(ctx, path, ReadOnly)
		if err != nil {
			return nil, err
		}
		if version != len(migrationFiles) {
			s.Close()
			return nil, fmt.Errorf("database %q still needs a schema upgrade", path)
		}
		return s, nil
	}

	if err = s.enableWAL(ctx); err == nil && version < len(migrationFiles) {
		_, err = s.migrate(ctx, false)
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	return s, nil
}

// Init creates a fresh schema in an empty database.
func Init(ctx context.Context, path string) (InitResult, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Initialized, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	switch {
	case err == nil:
		if err := f.Close(); err != nil {
			return Initialized, err
		}
	case !errors.Is(err, os.ErrExist):
		return Initialized, err
	}

	s, version, err := openSchema(ctx, path, ReadOnly)
	if err != nil {
		return Initialized, err
	}
	s.Close()
	if version == 0 {
		s, version, err = openSchema(ctx, path, Write)
		if err != nil {
			return Initialized, err
		}
		defer s.Close()
		if version == 0 {
			if err := s.enableWAL(ctx); err != nil {
				return Initialized, fmt.Errorf("initialize database %q: %w", path, err)
			}
			if version, err = s.migrate(ctx, true); err != nil {
				return Initialized, fmt.Errorf("initialize database %q: %w", path, err)
			}
		}
	}
	switch {
	case version == 0:
		return Initialized, nil
	case version < len(migrationFiles):
		return UpgradeRequired, nil
	default:
		return AlreadyInitialized, nil
	}
}

// Upgrade migrates an existing hostdb database if its schema is older.
func Upgrade(ctx context.Context, path string) error {
	s, version, err := openSchema(ctx, path, ReadOnly)
	if err != nil {
		return err
	}
	s.Close()
	if version == 0 || version == len(migrationFiles) {
		return nil
	}
	if err := upgrade(ctx, path); err != nil {
		return fmt.Errorf("upgrade database %q: %w", path, err)
	}
	return nil
}

// The caller has already checked that the database needs an upgrade.
func upgrade(ctx context.Context, path string) error {
	s, err := openDB(path, Write)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = s.migrate(ctx, false)
	return err
}

func openInitialized(ctx context.Context, path string, mode Mode) (*Store, int, error) {
	s, version, err := openSchema(ctx, path, mode)
	if err == nil && version == 0 {
		s.Close()
		return nil, 0, fmt.Errorf("database %q is not initialized. Run hostdb init", path)
	}
	return s, version, err
}

func openSchema(ctx context.Context, path string, mode Mode) (*Store, int, error) {
	if migrationFilesErr != nil {
		return nil, 0, migrationFilesErr
	}
	s, err := openDB(path, mode)
	if err != nil {
		return nil, 0, err
	}
	version, err := schemaVersion(ctx, s.db)
	if err != nil {
		s.Close()
		return nil, 0, fmt.Errorf("open database %q: %w", path, err)
	}
	return s, version, nil
}

func openDB(path string, mode Mode) (*Store, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("database %q does not exist. Run hostdb init", path)
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
	return &Store{Queries: Queries{db}, db: db}, nil
}

func (s *Store) enableWAL(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var journal string
		err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal)
		if err == nil {
			if !strings.EqualFold(journal, "wal") {
				return fmt.Errorf("could not enable WAL journal mode (got %s)", journal)
			}
			return nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
			return fmt.Errorf("enable WAL journal mode: %w", err)
		}
		// SQLite can report BUSY without waiting for busy_timeout when changing journal modes.
		select {
		case <-ctx.Done():
			return fmt.Errorf("enable WAL journal mode: %w", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *Store) migrate(ctx context.Context, initialize bool) (int, error) {
	var previousVersion int
	err := s.Transaction(ctx, func(q Queries) error {
		// Another process may have migrated the database while we waited for the lock.
		version, err := schemaVersion(ctx, q.db)
		if err != nil {
			return err
		}
		previousVersion = version
		empty := version == 0
		if initialize != empty {
			return nil
		}
		if initialize {
			if _, err := q.db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", applicationID)); err != nil {
				return err
			}
		}
		for i, file := range migrationFiles[version:] {
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
	return previousVersion, err
}

func schemaVersion(ctx context.Context, db runner) (int, error) {
	var version, appID int
	var unmarkedTables bool
	err := db.QueryRowContext(ctx, `
		SELECT user_version, application_id,
		       CASE WHEN user_version = 0 THEN EXISTS (
		           SELECT 1 FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'
		       ) ELSE 0 END
		FROM pragma_user_version(), pragma_application_id()
	`).Scan(&version, &appID, &unmarkedTables)
	if err != nil {
		return 0, err
	}
	if (version != 0 || appID != 0) && appID != applicationID {
		return 0, errors.New("SQLite file is not a hostdb database")
	}
	if version < 0 || version > len(migrationFiles) {
		return 0, fmt.Errorf("unsupported database schema %d (this hostdb supports through %d)", version, len(migrationFiles))
	}
	if unmarkedTables {
		return 0, errors.New("unrecognized SQLite database")
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
