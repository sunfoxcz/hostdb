package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnsibleIntegration(t *testing.T) {
	ansible, err := exec.LookPath("ansible")
	if err != nil {
		t.Skip("ansible is not installed")
	}
	inventory, err := exec.LookPath("ansible-inventory")
	if err != nil {
		t.Skip("ansible-inventory is not installed")
	}
	dir, _, cli := fixture(t)
	t.Setenv("PATH", filepath.Dir(cli)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ANSIBLE_CONFIG", filepath.Join(dir, "ansible.cfg"))
	t.Setenv("ANSIBLE_NOCOLOR", "true")
	t.Setenv("ANSIBLE_LOCAL_TEMP", filepath.Join(dir, "ansible-tmp"))
	t.Setenv("ANSIBLE_INVENTORY_ENABLED", "script")
	t.Setenv("ANSIBLE_SSH_ARGS", "-o ControlMaster=no")
	t.Setenv("ANSIBLE_SSH_TRANSFER_METHOD", "piped")
	cfg := "[defaults]\ninventory = ./inventory-hostdb\n"
	writeFile(t, filepath.Join(dir, "ansible.cfg"), cfg, 0600)
	wrapper := filepath.Join(dir, "inventory-hostdb")
	writeFile(t, wrapper, "#!/bin/sh\nexec hostdb inventory \"$@\"\n", 0700)
	writeFile(t, filepath.Join(dir, "group_vars", "all.yml"), "baseline_value: installed\n", 0600)
	writeFile(t, filepath.Join(dir, "group_vars", "php.yml"), "parent_value: inherited\nshared_value: parent\n", 0600)
	writeFile(t, filepath.Join(dir, "group_vars", "php56.yml"), "php_version: '5.6'\nshared_value: child\n", 0600)
	writeFile(t, filepath.Join(dir, "host_vars", "app-server.yml"), "host_setting: canonical\n", 0600)
	runCLI := func(args ...string) string { return mustRun(t, dir, cli, args...) }
	runCLI("init")
	runCLI("group", "add", "empty")
	empty, stderr, err := run(t, dir, inventory, "--list")
	need(t, err)
	if strings.Contains(stderr, "malformed group") || strings.Contains(empty, "\"hosts\": [") {
		t.Fatalf("empty inventory contains unexpected membership: %s\n%s", empty, stderr)
	}
	runCLI("host", "add", "app-server", "--hostname", "192.0.2.1", "--ssh-alias", "app-ssh", "--user", "unusable")
	runCLI("host", "add", "bare")
	runCLI("host", "add", "inactive", "--disabled")
	for _, name := range []string{"php", "php56", "backup"} {
		runCLI("group", "add", name)
	}
	runCLI("group", "child", "add", "php", "php56")
	runCLI("group", "child", "add", "backup", "php56")
	runCLI("group", "host", "add", "php56", "app-server")
	rawList := runCLI("inventory", "--list")
	if strings.Contains(rawList, "php_version") {
		t.Fatalf("hostdb unexpectedly loaded group_vars: %s", rawList)
	}
	graph, stderr, err := run(t, dir, inventory, "-i", "./inventory-hostdb", "--graph")
	need(t, err)
	if strings.Contains(stderr, "malformed group") {
		t.Fatalf("Ansible reported an invalid group: %s", stderr)
	}
	assertContains(t, graph, "@all:", "@ungrouped:", "bare", "@php:", "@backup:", "@php56:", "app-server")
	if strings.Contains(graph, "app-ssh") || strings.Contains(graph, "inactive") {
		t.Fatalf("unexpected alias or disabled host in graph: %s", graph)
	}
	var parsed struct {
		Meta struct {
			HostVars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	need(t, json.Unmarshal([]byte(mustRun(t, dir, inventory, "-i", "./inventory-hostdb", "--list")), &parsed))
	if len(parsed.Meta.HostVars) != 2 || parsed.Meta.HostVars["app-server"]["php_version"] != "5.6" {
		t.Fatalf("wrong resolved inventory: %+v", parsed)
	}
	direct := mustRun(t, dir, inventory, "--playbook-dir", dir, "-i", cli, "--host", "app-server")
	fromWrapper := mustRun(t, dir, inventory, "--host", "app-server")
	for _, out := range []string{direct, fromWrapper} {
		var vars map[string]any
		need(t, json.Unmarshal([]byte(out), &vars))
		for key, want := range map[string]string{
			"baseline_value": "installed", "parent_value": "inherited", "php_version": "5.6",
			"shared_value": "child", "host_setting": "canonical",
		} {
			if vars[key] != want {
				t.Fatalf("%s = %v, want %s (%s)", key, vars[key], want, out)
			}
		}
		for _, key := range []string{"ansible_host", "ansible_port", "ansible_user"} {
			if _, ok := vars[key]; ok {
				t.Fatalf("unexpected SSH override %s in %s", key, out)
			}
		}
	}
	for _, group := range []string{"php", "backup"} {
		out := mustRun(t, dir, ansible, group, "-m", "ansible.builtin.assert", "-a", "that=inventory_hostname=='app-server'")
		assertContains(t, out, "app-server | SUCCESS")
	}
	t.Run("native_ssh_ping_loopback", func(t *testing.T) {
		srv := localSSHD(t)
		runCLI("host", "set", "app-server", "hostname", "127.0.0.1")
		runCLI("host", "set", "app-server", "user", srv.user)
		runCLI("host", "set", "app-server", "port", srv.port)
		for _, pair := range [][2]string{
			{"IdentityFile", srv.key}, {"IdentitiesOnly", "yes"},
			{"UserKnownHostsFile", srv.knownHosts}, {"StrictHostKeyChecking", "yes"},
			{"BatchMode", "yes"},
		} {
			runCLI("ssh", "option", "set", "app-server", pair[0], pair[1])
		}
		generated := filepath.Join(dir, "hostdb.generated")
		runCLI("ssh", "sync", "--output", generated)
		ssh, err := exec.LookPath("ssh")
		need(t, err)
		for _, name := range []string{"app-server", "app-ssh"} {
			out := mustRun(t, dir, ssh, "-G", "-T", "-F", generated, name)
			assertContains(t, out, "hostname 127.0.0.1\n", "port "+srv.port+"\n", "user "+srv.user+"\n")
		}
		out := mustRun(t, dir, ansible, "app-server", "-c", "ssh", "-m", "ansible.builtin.ping", "--ssh-common-args", "-F "+generated)
		assertContains(t, out, "app-server | SUCCESS", "\"ping\": \"pong\"")
	})
}

type sshServer struct{ user, port, key, knownHosts string }

func localSSHD(t *testing.T) sshServer {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("OpenSSH server sshd is not installed")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client ssh is not installed")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	account, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine the loopback SSH user: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback sockets unavailable: %v", err)
	}
	address := listener.Addr().String()
	port := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	need(t, listener.Close())
	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_key")
	clientKey := filepath.Join(dir, "client_key")
	for _, path := range []string{hostKey, clientKey} {
		mustRun(t, dir, keygen, "-q", "-t", "ed25519", "-N", "", "-f", path)
	}
	hostPub, err := os.ReadFile(hostKey + ".pub")
	need(t, err)
	known := filepath.Join(dir, "known_hosts")
	writeFile(t, known, "[127.0.0.1]:"+port+" "+strings.TrimSpace(string(hostPub))+"\n", 0600)
	config := filepath.Join(dir, "sshd_config")
	writeFile(t, config, fmt.Sprintf("ListenAddress 127.0.0.1\nPort %s\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s.pub\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nPermitRootLogin prohibit-password\nSubsystem sftp internal-sftp\n",
		port, hostKey, filepath.Join(dir, "sshd.pid"), clientKey), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", config)
	log := &bytes.Buffer{}
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Skipf("cannot start a loopback sshd: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Skipf("loopback sshd is unavailable: %v: %s", err, log)
		default:
		}
		conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return sshServer{user: account.Username, port: port, key: clientKey, knownHosts: known}
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done
	t.Skipf("loopback sshd did not listen: %s", log)
	return sshServer{}
}
