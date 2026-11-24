package sshconfig

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostdb/internal/store"
)

func TestGenerateAdvancedConfig(t *testing.T) {
	hosts := []store.Host{
		{Name: "router-01", Hostname: "198.51.100.20", User: "netops", Port: 2222, Enabled: true, Options: []store.Option{
			{Name: "PubkeyAcceptedAlgorithms", Value: "+ssh-rsa"},
		}},
		{Name: "app-server", Hostname: "app-server.example.com", User: "root", Port: 22, Enabled: true, Aliases: []string{"app-alt", "app-ssh"}, Options: []store.Option{
			{Name: "IdentityFile", Value: "\"~/.ssh/with space\""},
			{Name: "IdentityFile", Value: "~/.ssh/extra"},
		}},
		{Name: "switch-01", Hostname: "192.0.2.85", User: "root", Port: 22, Enabled: true, Options: []store.Option{
			{Name: "PasswordAuthentication", Value: "yes"},
			{Name: "KexAlgorithms", Value: "+diffie-hellman-group1-sha1"},
			{Name: "HostKeyAlgorithms", Value: "+ssh-rsa"},
			{Name: "MACs", Value: "hmac-sha1"},
			{Name: "Ciphers", Value: "aes128-ctr"},
		}},
		{Name: "omitted", Hostname: "omitted", Port: 22},
	}
	want := Header + "\nHost router-01\n    HostName 198.51.100.20\n    User netops\n    Port 2222\n    PubkeyAcceptedAlgorithms +ssh-rsa\n" +
		"\nHost app-server app-alt app-ssh\n    HostName app-server.example.com\n    User root\n    Port 22\n    IdentityFile \"~/.ssh/with space\"\n    IdentityFile ~/.ssh/extra\n" +
		"\nHost switch-01\n    HostName 192.0.2.85\n    User root\n    Port 22\n    PasswordAuthentication yes\n    KexAlgorithms +diffie-hellman-group1-sha1\n    HostKeyAlgorithms +ssh-rsa\n    MACs hmac-sha1\n    Ciphers aes128-ctr\n"
	got, err := Generate(hosts)
	if err != nil || string(got) != want {
		t.Fatalf("generate: %v\n%s", err, got)
	}
	hosts[0].Options = append(hosts[0].Options, store.Option{Name: "IdentityFile", Value: "test\nInclude bad"})
	if _, err := Generate(hosts); err == nil {
		t.Fatal("generator accepted an unsafe database value")
	}
}

func TestSyncAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.d", "hostdb.generated")
	first := []store.Host{{Name: "first", Hostname: "first.example", Port: 22, Enabled: true}}
	next := []store.Host{{Name: "next", Hostname: "next.example", Port: 22, Enabled: true}}
	firstConfig, err := Generate(first)
	if err != nil {
		t.Fatal(err)
	}
	nextConfig, err := Generate(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), path, first); err != nil {
		t.Fatal(err)
	}
	open, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if err := Sync(context.Background(), path, next); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(open)
	if err != nil || !bytes.Equal(old, firstConfig) {
		t.Fatalf("existing reader saw partial replacement: %v, %q", err, old)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, nextConfig) {
		t.Fatalf("new reader did not see complete replacement: %v, %q", err, got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("bad file mode: %v %v", info, err)
	}
	manual := filepath.Join(dir, "config")
	if err := os.WriteFile(manual, []byte("# manual\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), manual, next); err == nil {
		t.Fatal("overwrote manual config")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := Sync(context.Background(), link, first); err == nil {
		t.Fatal("overwrote a symlink")
	}
}

func TestCheckDoesNotEvaluateMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("Match exec \"touch "+filepath.Join(dir, "ran")+"\"\n    User wrong\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := configSnapshot{dir: dir, base: dir, copies: map[string]string{}, visiting: map[string]bool{}}
	if _, err := snapshot.copy(path); err == nil || !strings.Contains(err.Error(), "Match section") {
		t.Fatalf("unsafe Match was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Fatalf("unexpected command side effect: %v", err)
	}
}
