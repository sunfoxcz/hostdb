package importer

import (
	"reflect"
	"strings"
	"testing"

	"hostdb/internal/store"
)

func TestSSHParse(t *testing.T) {
	input := "# SSH\nInclude other\nHost *\n User ignored\nHost app-ssh app-server\n HostName=\"app-server.example.com\" # old\n User root\n IdentityFile \"~/.ssh/first key\"\n IdentityFile ~/.ssh/second\n KexAlgorithms +diffie-hellman-group1-sha1\nMatch user a\n Port 111\n"
	p, err := SSH(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.hosts) != 1 || len(p.Warnings) < 3 {
		t.Fatalf("unexpected result: %+v", p)
	}
	h := p.hosts[0]
	if !reflect.DeepEqual(h.names, []string{"app-ssh", "app-server"}) || h.address != "app-server.example.com" || h.user != "root" ||
		len(h.options) != 3 || h.options[0].Value != "\"~/.ssh/first key\"" || h.options[2].Value != "+diffie-hellman-group1-sha1" {
		t.Fatalf("unexpected SSH record: %+v", h)
	}
}

func TestAnsibleParse(t *testing.T) {
	input := "[all:children]\nphp\n[php:children]\nphp56\n[php56]\nweb01:2200 ansible_host='web01.example' ansible_user=root custom='secret data'\n[php56:vars]\nphp_version=5.6\n[backup]\nweb01 ansible_ssh_port=2200\n"
	p, err := AnsibleINI(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.groups) != 3 || len(p.hosts) != 1 || len(p.memberships) != 2 ||
		!reflect.DeepEqual(p.children, []store.Pair{{First: "php", Second: "php56"}}) {
		t.Fatalf("unexpected result: %+v", p)
	}
	h := p.hosts[0]
	if h.address != "web01.example" || h.port != 2200 || h.user != "root" || len(p.Warnings) != 2 || strings.Contains(strings.Join(p.Warnings, "\n"), "secret data") {
		t.Fatalf("unexpected host or warning: %+v %v", h, p.Warnings)
	}
	if _, err := AnsibleINI(strings.NewReader("[php]\nweb01 ansible_port=22\nweb01 ansible_port=23\n")); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("expected conflict, got %v", err)
	}
}
