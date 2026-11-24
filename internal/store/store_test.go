package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testStore(t testing.TB) (*Store, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "hosts?#.db")
	_, err := Init(ctx, path)
	check(t, err)
	s, err := Open(ctx, path, Write)
	check(t, err)
	t.Cleanup(func() { s.Close() })
	return s, path, ctx
}

func check(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustFail(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("expected error containing %q, got %v", text, err)
	}
}

func TestHostLifecycle(t *testing.T) {
	s, _, ctx := testStore(t)
	check(t, s.Transaction(ctx, func(q Queries) error {
		id, err := q.CreateHost(ctx, Host{Name: "app-server", Hostname: "app-server.example.com", User: "root", Port: 22, Enabled: true})
		check(t, err)
		check(t, q.Alias(ctx, id, "app-ssh", true))
		check(t, q.AddGroup(ctx, "ubuntu24", ""))
		check(t, q.Link(ctx, "ubuntu24", "app-server", false, true))
		for _, key := range []string{"~/.ssh/id_one", "~/.ssh/id_two"} {
			check(t, q.Option(ctx, id, "add", "IdentityFile", &key))
		}
		check(t, q.SetHost(ctx, "app-server", "hostname", "new.example.com"))
		check(t, q.SetHost(ctx, "app-server", "port", "2200"))
		check(t, q.SetHost(ctx, "app-server", "name", "new-name"))
		host, err := q.HostDetails(ctx, "new-name")
		check(t, err)
		if host.ID != id || host.Hostname != "new.example.com" || host.Port != 2200 || !host.Enabled ||
			len(host.Options) != 2 || !reflect.DeepEqual(host.Aliases, []string{"app-ssh"}) ||
			!reflect.DeepEqual(host.Groups, []string{"ubuntu24"}) || host.CreatedAt == "" || host.UpdatedAt == "" {
			t.Fatalf("bad host: %+v", host)
		}
		found, exists, err := q.HostBySSHName(ctx, "app-ssh")
		check(t, err)
		if !exists || found.Name != "new-name" {
			t.Fatalf("alias resolved to %+v", found)
		}
		replacement := "~/.ssh/id_new"
		check(t, q.Option(ctx, id, "set", "identityfile", &replacement))
		opts, err := q.Options(ctx, id)
		check(t, err)
		if len(opts) != 1 || opts[0].Value != replacement || opts[0].Position != 0 {
			t.Fatalf("bad option replacement: %+v", opts)
		}
		check(t, q.Option(ctx, id, "remove", "IdentityFile", &replacement))
		check(t, q.RemoveHost(ctx, "new-name"))
		_, exists, err = q.HostBySSHName(ctx, "app-ssh")
		check(t, err)
		if exists {
			t.Fatal("cascading deletion did not delete alias")
		}
		group, err := q.GroupDetails(ctx, "ubuntu24")
		check(t, err)
		if len(group.Hosts) != 0 {
			t.Fatalf("cascading deletion did not delete membership: %+v", group)
		}
		return q.Validate(ctx)
	}))
}

func TestConflictsAndAtomicity(t *testing.T) {
	s, _, ctx := testStore(t)
	h := Host{Name: "first", Hostname: "first.example", Port: 22, Enabled: true}
	id, err := s.CreateHost(ctx, h)
	check(t, err)
	check(t, s.Alias(ctx, id, "first-alias", true))
	mustFail(t, s.Alias(ctx, id, "first-alias", true), "UNIQUE")
	mustFail(t, s.Alias(ctx, id, "first", true), "canonical host name")
	h.Name = "first-alias"
	_, err = s.CreateHost(ctx, h)
	mustFail(t, err, "used as SSH alias")
	err = s.Transaction(ctx, func(q Queries) error {
		h.Name = "second"
		id, err := q.CreateHost(ctx, h)
		if err != nil {
			return err
		}
		return q.Alias(ctx, id, "first-alias", true)
	})
	mustFail(t, err, "UNIQUE")
	_, err = s.Host(ctx, "second")
	mustFail(t, err, "not found")
	mustFail(t, s.SetHost(ctx, "first", "port", "65536"), "invalid SSH port")
	mustFail(t, s.SetHost(ctx, "first", "name", "first-alias"), "used as SSH alias")
}

func TestGroupDAG(t *testing.T) {
	s, _, ctx := testStore(t)
	_, err := s.CreateHost(ctx, Host{Name: "web01", Hostname: "web01.example", Port: 22, Enabled: true})
	check(t, err)
	for _, name := range []string{"infra", "php", "legacy", "php56"} {
		check(t, s.AddGroup(ctx, name, ""))
	}
	for _, edge := range []Pair{{"infra", "php"}, {"infra", "legacy"}, {"php", "php56"}, {"legacy", "php56"}} {
		check(t, s.Link(ctx, edge.First, edge.Second, true, true))
	}
	check(t, s.Link(ctx, "php56", "web01", false, true))
	check(t, s.Link(ctx, "legacy", "web01", false, true))
	mustFail(t, s.Link(ctx, "php56", "infra", true, true), "cycle")
	mustFail(t, s.Link(ctx, "php", "php", true, true), "cycle")
	mustFail(t, s.Link(ctx, "php", "absent", true, true), "not found")
	mustFail(t, s.Link(ctx, "php", "php56", true, true), "UNIQUE")
	host, err := s.HostDetails(ctx, "web01")
	check(t, err)
	if !reflect.DeepEqual(host.Groups, []string{"legacy", "php56"}) {
		t.Fatalf("unexpected direct memberships: %v", host.Groups)
	}
	group, err := s.GroupDetails(ctx, "php56")
	check(t, err)
	if !reflect.DeepEqual(group.Parents, []string{"legacy", "php"}) {
		t.Fatalf("unexpected parents: %v", group.Parents)
	}
	check(t, s.Validate(ctx))
	check(t, s.Link(ctx, "php", "php56", true, false))
	check(t, s.Link(ctx, "legacy", "web01", false, false))
	check(t, s.RemoveGroup(ctx, "php"))
	check(t, s.Validate(ctx))
}

func TestMigrationAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	check(t, os.WriteFile(path, nil, 0600))
	db, err := sql.Open("sqlite", path)
	check(t, err)
	t.Cleanup(func() { db.Close() })
	migration, err := migrations.ReadFile("migrations/001_inventory.sql")
	check(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	check(t, err)
	_, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=1", applicationID))
	check(t, err)
	legacy := Queries{db}
	first, err := legacy.CreateHost(ctx, Host{Name: "before", Hostname: "before", Port: 22, Enabled: true})
	check(t, err)
	check(t, legacy.Alias(ctx, first, "before-alias", true))
	check(t, legacy.AddGroup(ctx, "before", ""))
	check(t, legacy.AddGroup(ctx, "before-alias", ""))
	check(t, db.Close())
	status, err := Init(ctx, path)
	check(t, err)
	if status != UpgradeRequired {
		t.Fatalf("init returned %v for an older schema", status)
	}
	check(t, Upgrade(ctx, path))
	migrated, err := Open(ctx, path, Write)
	check(t, err)
	defer migrated.Close()
	h, err := migrated.Host(ctx, "before")
	check(t, err)
	if h.ID != first {
		t.Fatalf("migration changed the host: %+v", h)
	}
	validationErr := migrated.Validate(ctx)
	mustFail(t, validationErr, `group "before" conflicts with a canonical host name`)
	mustFail(t, validationErr, `group "before-alias" conflicts with an SSH alias of host "before"`)
	check(t, migrated.Alias(ctx, first, "before-alias", false))
	mustFail(t, migrated.SetHost(ctx, "before", "name", "before-alias"), "used as group name")
	check(t, migrated.SetHost(ctx, "before", "name", "renamed"))
	check(t, migrated.Validate(ctx))
	_, err = migrated.CreateHost(ctx, Host{Name: "in-wal", Hostname: "in-wal", Port: 22, Enabled: true})
	check(t, err)
	wal, err := os.Stat(path + "-wal")
	check(t, err)
	if wal.Size() == 0 {
		t.Fatal("test needs committed data in WAL")
	}
	backup := filepath.Join(t.TempDir(), "copy.db")
	reader, err := Open(ctx, path, ReadOnly)
	check(t, err)
	defer reader.Close()
	check(t, reader.Backup(ctx, backup))
	mustFail(t, reader.Backup(ctx, backup), "already exists")
	info, err := os.Stat(backup)
	check(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode: %o", info.Mode().Perm())
	}
	copy, err := Open(ctx, backup, ReadOnly)
	check(t, err)
	defer copy.Close()
	_, err = copy.Host(ctx, "in-wal")
	check(t, err)
	check(t, copy.Validate(ctx))
	mustFail(t, copy.SetHost(ctx, "renamed", "port", "23"), "readonly")
}

func TestDBPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/path")
	t.Setenv("HOSTDB_DB", "")
	path, err := Path("")
	check(t, err)
	if path != filepath.Join(home, ".local", "share", "hostdb", "hosts.db") {
		t.Fatal(path)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	path, err = Path("")
	check(t, err)
	if path != filepath.Join(xdg, "hostdb", "hosts.db") {
		t.Fatal(path)
	}
	viaEnv := filepath.Join(xdg, "env.db")
	t.Setenv("HOSTDB_DB", viaEnv)
	path, err = Path("")
	check(t, err)
	if path != viaEnv {
		t.Fatal(path)
	}
	path, err = Path(filepath.Join(xdg, "flag.db"))
	check(t, err)
	if path != filepath.Join(xdg, "flag.db") {
		t.Fatal(path)
	}
}

func TestRejectInjectedConfigAndBadPermissions(t *testing.T) {
	s, path, ctx := testStore(t)
	id, err := s.CreateHost(ctx, Host{Name: "node", Hostname: "node.example", Port: 22, Enabled: true})
	check(t, err)
	for _, key := range []string{"Match", "hOst", "Include", "User", "Port=22", "IgnoreUnknown", "Password"} {
		value := "any"
		if err := s.Option(ctx, id, "add", key, &value); err == nil {
			t.Errorf("accepted SSH option %q", key)
		}
	}
	for _, value := range []string{"sometimes", "yes\nHost *", "yes\rHost *", "\x00", " ", "yes\u2028Host *", "# option"} {
		if err := s.Option(ctx, id, "add", "PasswordAuthentication", &value); err == nil {
			t.Errorf("accepted value %q", value)
		}
	}
	value := "TRUE"
	check(t, s.Option(ctx, id, "add", "PasswordAuthentication", &value))
	if err := s.Alias(ctx, id, "*", true); err == nil {
		t.Fatal("accepted wildcard alias")
	}
	check(t, s.Close())
	check(t, os.Chmod(path, 0644))
	bad, err := Open(ctx, path, ReadOnly)
	if bad != nil {
		bad.Close()
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a permissions error, got %v", err)
	}
}
