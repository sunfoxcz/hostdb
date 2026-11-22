package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var buildBinary = sync.OnceValues(func() (string, error) {
	if path := os.Getenv("HOSTDB_TEST_BINARY"); path != "" {
		return filepath.Abs(path)
	}
	dir, err := os.MkdirTemp("", "hostdb-tests-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "hostdb")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/hostdb")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	builtDir = dir
	return binary, nil
})

var builtDir string

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

func need(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func binary(t testing.TB) string {
	t.Helper()
	path, err := buildBinary()
	need(t, err)
	return path
}

func writeFile(t testing.TB, path, content string, mode os.FileMode) {
	t.Helper()
	need(t, os.MkdirAll(filepath.Dir(path), 0700))
	need(t, os.WriteFile(path, []byte(content), mode))
}

func run(t testing.TB, dir, exe string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s timed out: %s\n%s", exe, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), err
}

func mustRun(t testing.TB, dir, exe string, args ...string) string {
	t.Helper()
	out, stderr, err := run(t, dir, exe, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\nstdout: %s\nstderr: %s", exe, args, err, out, stderr)
	}
	return out
}

func assertContains(t testing.TB, got string, want ...string) {
	t.Helper()
	for _, value := range want {
		if !strings.Contains(got, value) {
			t.Fatalf("output does not contain %q:\n%s", value, got)
		}
	}
}

func fixture(t *testing.T) (dir, db, cli string) {
	t.Helper()
	cli = binary(t)
	dir = t.TempDir()
	need(t, os.Chmod(dir, 0700))
	db = filepath.Join(dir, "hosts.db")
	t.Setenv("HOSTDB_DB", db)
	t.Setenv("HOME", dir)
	return dir, db, cli
}

func TestCLIInventoryAndSSH(t *testing.T) {
	dir, db, cli := fixture(t)
	runCLI := func(args ...string) string { return mustRun(t, dir, cli, args...) }
	runCLI("init")
	runCLI("host", "add", "app-server", "--hostname", "app-server.example.com", "--ssh-alias", "app-ssh", "--user", "root")
	runCLI("host", "add", "ungrouped-node")
	runCLI("host", "add", "inactive", "--disabled", "--ssh-alias", "inactive-alias")
	for _, name := range []string{"php", "php56", "ubuntu24", "empty"} {
		runCLI("group", "add", name)
	}
	runCLI("group", "child", "add", "php", "php56")
	runCLI("group", "host", "add", "php56", "app-server")
	runCLI("group", "host", "add", "ubuntu24", "app-server")
	runCLI("group", "host", "add", "ubuntu24", "inactive")
	runCLI("init")
	before, err := os.ReadFile(db)
	need(t, err)
	out, stderr, err := run(t, dir, cli, "--list")
	need(t, err)
	if stderr != "" || out != runCLI("inventory", "--list") {
		t.Fatalf("unexpected dynamic inventory output: %s %s", out, stderr)
	}
	want := map[string]any{
		"_meta":     map[string]any{"hostvars": map[string]any{"app-server": map[string]any{}, "ungrouped-node": map[string]any{}}},
		"all":       map[string]any{"hosts": []any{}, "children": []any{"ungrouped", "empty", "php", "ubuntu24"}},
		"ungrouped": map[string]any{"hosts": []any{"ungrouped-node"}},
		"php":       map[string]any{"hosts": []any{}, "children": []any{"php56"}},
		"php56":     map[string]any{"hosts": []any{"app-server"}},
		"ubuntu24":  map[string]any{"hosts": []any{"app-server"}},
		"empty":     map[string]any{"hosts": []any{}},
	}
	var got map[string]any
	need(t, json.Unmarshal([]byte(out), &got))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory mismatch: %s", out)
	}
	for _, name := range []string{"app-server", "app-ssh", "inactive", "absent"} {
		if got := runCLI("inventory", "--host", name); got != "{}\n" || got != runCLI("--host", name) {
			t.Fatalf("host lookup %q: %s", name, got)
		}
	}
	after, err := os.ReadFile(db)
	need(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("inventory wrote to database")
	}
	if out, _, err := run(t, dir, cli, "--db", filepath.Join(dir, "absent.db"), "--list"); out != "" || err == nil {
		t.Fatalf("bad inventory failure behavior: %q, %v", out, err)
	}
	if got := runCLI("completion", "targets"); got != "all\napp-server\nempty\nphp\nphp56\nubuntu24\nungrouped\nungrouped-node\n" {
		t.Fatalf("bad targets: %q", got)
	}
	if got := runCLI("completion", "ssh-aliases"); got != "app-ssh\n" {
		t.Fatalf("bad alias completion: %q", got)
	}
	assertContains(t, runCLI("__completeNoDesc", "host", "show", "app"), "app-server")
	assertContains(t, runCLI("__completeNoDesc", "group", "host", "add", "ubuntu24", "app"), "app-server")
	assertContains(t, runCLI("__completeNoDesc", "group", "show", "ph"), "php56")
	assertContains(t, runCLI("host", "list", "--format", "names"), "inactive\n", "app-server\n")
	if !json.Valid([]byte(runCLI("host", "list", "--format", "json"))) {
		t.Fatal("host list returned invalid JSON")
	}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		script := filepath.Join(dir, "_hostdb")
		writeFile(t, script, runCLI("completion", "zsh"), 0600)
		mustRun(t, dir, zsh, "-n", script)
	}

	runCLI("ssh", "sync")
	generated := filepath.Join(dir, ".ssh", "config.d", "hostdb.generated")
	manual := filepath.Join(dir, ".ssh", "config")
	writeFile(t, manual, "Include config.d/hostdb.generated\n\nHost *\n    User fallback\n", 0600)
	runCLI("ssh", "option", "add", "app-server", "IdentityFile", "~/.ssh/test-a")
	runCLI("ssh", "option", "add", "app-server", "IdentityFile", "~/.ssh/test-b")
	runCLI("ssh", "option", "set", "app-server", "ServerAliveInterval", "30")
	cfg, err := os.ReadFile(generated)
	need(t, err)
	if string(cfg) != runCLI("ssh", "generate") {
		t.Fatalf("auto-sync out of date: %s", cfg)
	}
	assertContains(t, string(cfg), "Host app-server app-ssh", "IdentityFile ~/.ssh/test-a\n    IdentityFile ~/.ssh/test-b")
	if ssh, err := exec.LookPath("ssh"); err == nil {
		// OpenSSH obtains its implicit home directory from passwd rather than HOME.
		probe := filepath.Join(dir, "probe.conf")
		writeFile(t, probe, "Include \""+generated+"\"\nHost *\n    User fallback\n", 0600)
		for _, name := range []string{"app-server", "app-ssh"} {
			assertContains(t, mustRun(t, dir, ssh, "-G", "-T", "-F", probe, name),
				"hostname app-server.example.com\n", "user root\n", "port 22\n", "serveraliveinterval 30\n")
		}
		assertContains(t, runCLI("doctor"), "Generated SSH config is current", "OpenSSH resolved 3")
		_, stderr, err := run(t, dir, cli, "ssh", "option", "set", "app-server", "KexAlgorithms", "+hostdb-test-unavailable")
		if err == nil {
			t.Fatal("synchronized a KEX algorithm unavailable to OpenSSH")
		}
		assertContains(t, stderr, "database changes committed", "OpenSSH rejected", "hostdb-test-unavailable")
		unchanged, err := os.ReadFile(generated)
		need(t, err)
		if !bytes.Equal(cfg, unchanged) {
			t.Fatal("an OpenSSH parse error replaced the published configuration")
		}
		assertContains(t, runCLI("ssh", "generate"), "KexAlgorithms +hostdb-test-unavailable")
		_, stderr, err = run(t, dir, cli, "doctor")
		if err == nil {
			t.Fatal("doctor missed the unavailable KEX algorithm")
		}
		assertContains(t, stderr, "OpenSSH rejected settings for", "hostdb-test-unavailable")
		runCLI("ssh", "option", "remove", "app-server", "KexAlgorithms")
		writeFile(t, manual, "Host *\n    User override\nInclude config.d/hostdb.generated\n", 0600)
		_, stderr, err = run(t, dir, cli, "doctor")
		if err == nil {
			t.Fatal("doctor missed a manual config conflict")
		}
		assertContains(t, stderr, "effective user differs")
	}
	writeFile(t, generated, "# manual replacement\n", 0600)
	_, stderr, err = run(t, dir, cli, "host", "set", "app-server", "port", "2200")
	if err == nil {
		t.Fatal("expected an automatic sync failure")
	}
	assertContains(t, stderr, "database changes committed", "refusing to overwrite")
	assertContains(t, runCLI("host", "show", "app-server"), "port: 2200")
	cfg, err = os.ReadFile(generated)
	need(t, err)
	if string(cfg) != "# manual replacement\n" {
		t.Fatal("sync failure changed the existing file")
	}
	runCLI("ssh", "disable-auto-sync")
	runCLI("host", "disable", "app-server")
	if got := runCLI("--list"); strings.Contains(got, "\"app-server\"") {
		t.Fatalf("disabled host remains in inventory: %s", got)
	}
	runCLI("host", "enable", "app-server")
	runCLI("host", "rename", "app-server", "app-ssh")
	assertContains(t, runCLI("host", "show", "app-ssh"), "groups: php56, ubuntu24")
	runCLI("validate")
}

func TestCLIHostListGroup(t *testing.T) {
	dir, _, cli := fixture(t)
	runCLI := func(args ...string) string { return mustRun(t, dir, cli, args...) }
	runCLI("init")
	runCLI("host", "add", "direct-node")
	runCLI("host", "add", "shared-node", "--ssh-alias", "shared-ssh")
	runCLI("host", "add", "disabled-node", "--disabled")
	runCLI("host", "add", "ungrouped-node")
	for _, group := range []string{"app", "blue", "green", "shared", "empty"} {
		runCLI("group", "add", group)
	}
	for _, edge := range [][2]string{{"app", "blue"}, {"app", "green"}, {"blue", "shared"}, {"green", "shared"}} {
		runCLI("group", "child", "add", edge[0], edge[1])
	}
	runCLI("group", "host", "add", "app", "direct-node")
	runCLI("group", "host", "add", "blue", "shared-node")
	runCLI("group", "host", "add", "shared", "shared-node")
	runCLI("group", "host", "add", "shared", "disabled-node")

	for group, want := range map[string]string{
		"app":       "direct-node\ndisabled-node\nshared-node\n",
		"blue":      "disabled-node\nshared-node\n",
		"empty":     "",
		"all":       "direct-node\ndisabled-node\nshared-node\nungrouped-node\n",
		"ungrouped": "ungrouped-node\n",
	} {
		if got := runCLI("host", "list", "--group", group, "--format", "names"); got != want {
			t.Errorf("host names for group %q: got %q, want %q", group, got, want)
		}
	}
	table := runCLI("host", "list", "--group", "green")
	assertContains(t, table, "NAME", "disabled-node", "shared-node")
	if strings.Contains(table, "direct-node") || strings.Contains(table, "ungrouped-node") {
		t.Errorf("table contains a host outside the group:\n%s", table)
	}

	type listedHost struct {
		Name    string
		Enabled bool
	}
	var hosts []listedHost
	out := runCLI("host", "list", "--group", "shared", "--format", "json")
	need(t, json.Unmarshal([]byte(out), &hosts))
	wantHosts := []listedHost{{Name: "disabled-node"}, {Name: "shared-node", Enabled: true}}
	if !reflect.DeepEqual(hosts, wantHosts) {
		t.Errorf("unexpected JSON host list: %s", out)
	}
	if got := runCLI("host", "list", "--group", "empty", "--format", "json"); got != "[]\n" {
		t.Errorf("expected an empty JSON array, got %q", got)
	}
	out, stderr, err := run(t, dir, cli, "host", "list", "--group", "missing", "--format", "json")
	if out != "" || err == nil {
		t.Fatalf("expected missing group to fail without stdout, got %q, %v", out, err)
	}
	assertContains(t, stderr, `group "missing" not found`)
	assertContains(t, runCLI("__completeNoDesc", "host", "list", "--group", "sh"), "shared")
	assertContains(t, runCLI("__completeNoDesc", "host", "list", "--group", "un"), "ungrouped")
}

func TestImportTransactionAndMapping(t *testing.T) {
	dir, db, cli := fixture(t)
	runCLI := func(args ...string) string { return mustRun(t, dir, cli, args...) }
	ini := filepath.Join(dir, "inventory.ini")
	ssh := filepath.Join(dir, "config")
	writeFile(t, ini, "[php:children]\nphp56\n[php56]\napp-server ansible_host=app-server.example.com ansible_user=root custom=secret\n", 0600)
	writeFile(t, ssh, "Host app-ssh app-alt\n HostName app-server.example.com\n User root\n IdentityFile ~/.ssh/first\n IdentityFile ~/.ssh/second\n HostKeyAlgorithms +ssh-rsa\n", 0600)
	out, stderr, err := run(t, dir, cli, "import", "ansible-ini", ini, "--dry-run")
	need(t, err)
	assertContains(t, out, "Dry run", "add group php", "add host app-server")
	assertContains(t, stderr, "\"custom\"", "host_vars")
	if strings.Contains(stderr, "secret") {
		t.Fatal("warning leaked variable value")
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("dry run created a database: %v", err)
	}
	runCLI("init")
	runCLI("import", "ansible-ini", ini)
	if got := runCLI("import", "ansible-ini", ini); got != "No changes.\n" {
		t.Fatalf("import is not idempotent: %s", got)
	}
	_, stderr, err = run(t, dir, cli, "import", "ssh-config", ssh)
	if err == nil {
		t.Fatal("unmapped shared address was imported")
	}
	assertContains(t, stderr, "address", "map the SSH name")
	assertContains(t, runCLI("import", "ssh-config", ssh, "--dry-run", "--map", "app-ssh=app-server"), "add SSH alias app-ssh")
	if got := runCLI("host", "show", "app-server"); strings.Contains(got, "app-ssh") {
		t.Fatalf("dry run mutated database: %s", got)
	}
	runCLI("import", "ssh-config", ssh, "--map", "app-ssh=app-server")
	if got := runCLI("import", "ssh-config", ssh); got != "No changes.\n" {
		t.Fatalf("SSH import is not idempotent: %s", got)
	}
	assertContains(t, runCLI("ssh", "generate"), "Host app-server app-alt app-ssh\n", "IdentityFile ~/.ssh/first\n    IdentityFile ~/.ssh/second\n    HostKeyAlgorithms +ssh-rsa\n")
	if got := runCLI("host", "list", "--format", "names"); got != "app-server\n" {
		t.Fatalf("unexpected canonical hosts: %q", got)
	}
	writeFile(t, ini, "[alpha:children]\nbeta\n[beta:children]\nalpha\n[beta]\nshould-rollback\n", 0600)
	_, stderr, err = run(t, dir, cli, "import", "ansible-ini", ini)
	if err == nil {
		t.Fatal("cyclic import succeeded")
	}
	assertContains(t, stderr, "cycle")
	if got := runCLI("group", "list", "--format", "names"); got != "php\nphp56\n" {
		t.Fatalf("failed import left groups: %s", got)
	}
	if got := runCLI("host", "list", "--format", "names"); got != "app-server\n" {
		t.Fatalf("failed import left hosts: %q", got)
	}
}
