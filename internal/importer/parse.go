package importer

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"hostdb/internal/configtext"
	"hostdb/internal/sshconfig"
	"hostdb/internal/store"
)

type record struct {
	names   []string
	address string
	user    string
	port    int
	hasUser bool
	options []store.Option
	line    int
}

type Plan struct {
	SSH         bool
	hosts       []*record
	groups      map[string]bool
	children    []store.Pair
	memberships []store.Pair
	Warnings    []string
}

func newPlan(ssh bool) *Plan {
	return &Plan{SSH: ssh, groups: map[string]bool{}}
}

func lines(r io.Reader, visit func(int, string) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	n := 0
	for scanner.Scan() {
		n++
		line := scanner.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		if err := visit(n, line); err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
	}
	return scanner.Err()
}

func (p *Plan) warn(line int, format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
}

func setField(host *record, key, value string) error {
	switch key {
	case "hostname", "ansible_host":
		if err := store.Address(value); err != nil {
			return err
		}
		if host.address != "" && host.address != value {
			return fmt.Errorf("conflicting addresses for %q", host.names[0])
		}
		host.address = value
	case "user", "ansible_user":
		if err := store.User(value); err != nil {
			return err
		}
		if host.hasUser && host.user != value {
			return fmt.Errorf("conflicting SSH users for %q", host.names[0])
		}
		host.user, host.hasUser = value, true
	case "port", "ansible_port":
		port, err := strconv.Atoi(value)
		if err != nil || store.Port(port) != nil {
			return fmt.Errorf("invalid SSH port %q", value)
		}
		if host.port != 0 && host.port != port {
			return fmt.Errorf("conflicting SSH ports for %q", host.names[0])
		}
		host.port = port
	}
	return nil
}

func SSH(r io.Reader) (*Plan, error) {
	p := newPlan(true)
	var host *record
	err := lines(r, func(n int, line string) error {
		key, value := sshconfig.Directive(line)
		if key == "" {
			return nil
		}
		switch lower := strings.ToLower(key); lower {
		case "host":
			host = nil
			names, err := configtext.Words(value, false)
			if err != nil {
				return err
			}
			if len(names) == 0 {
				return fmt.Errorf("empty Host directive")
			}
			seen := map[string]bool{}
			var distinct []string
			for _, name := range names {
				if strings.ContainsAny(name, "*?!") {
					p.warn(n, "skipped Host block with conditional patterns")
					return nil
				}
				name = strings.ToLower(name)
				if err := store.HostName(name); err != nil {
					return err
				}
				if seen[name] {
					p.warn(n, "duplicate SSH name %q", name)
					continue
				}
				seen[name] = true
				distinct = append(distinct, name)
			}
			host = &record{names: distinct, line: n}
			p.hosts = append(p.hosts, host)
		case "match":
			host = nil
			p.warn(n, "skipped Match block")
		case "include", "ignoreunknown":
			p.warn(n, "skipped directive %s", key)
		default:
			if host == nil {
				p.warn(n, "skipped directive %s outside an explicit Host block", key)
				return nil
			}
			switch lower {
			case "hostname", "user", "port":
				words, err := configtext.Words(value, false)
				if err != nil {
					return err
				}
				if len(words) != 1 {
					return fmt.Errorf("%s requires a single argument", key)
				}
				return setField(host, lower, words[0])
			default:
				if err := store.OptionName(key); err != nil {
					return err
				}
				if err := store.OptionValue(key, value); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				host.options = append(host.options, store.Option{Name: key, Value: value})
			}
		}
		return nil
	})
	if err == nil {
		for _, record := range p.hosts {
			if len(record.names) > 1 && record.address == "" {
				return p, fmt.Errorf("line %d: Host block %q lists several names without HostName; specify a shared address or split the block", record.line, record.names[0])
			}
		}
	}
	return p, err
}

func AnsibleINI(r io.Reader) (*Plan, error) {
	p := newPlan(false)
	section, group := "hosts", ""
	hosts := map[string]*record{}
	sections := map[string]bool{}
	seenEdges := map[store.Pair]bool{}
	seenMembers := map[store.Pair]bool{}
	addGroup := func(name string) error {
		if name == "all" || name == "ungrouped" {
			return nil
		}
		if err := store.GroupName(name); err != nil {
			return err
		}
		p.groups[name] = true
		return nil
	}
	err := lines(r, func(n int, line string) error {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			return nil
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			i := strings.IndexByte(line, ']')
			tail := strings.TrimSpace(line[i+1:])
			// A bracketed IPv6/range host followed by :port is not a section.
			if tail == "" || strings.HasPrefix(tail, "#") || strings.HasPrefix(tail, ";") {
				header := line[1:i]
				var suffix string
				group, suffix, _ = strings.Cut(header, ":")
				if err := addGroup(group); err != nil {
					return err
				}
				if sections[header] {
					p.warn(n, "repeated section [%s]", header)
				}
				sections[header] = true
				switch suffix {
				case "":
					section = "hosts"
				case "children", "vars":
					section = suffix
				default:
					return fmt.Errorf("unsupported section [%s]", header)
				}
				return nil
			}
		}
		if section == "vars" {
			key, _, _ := strings.Cut(line, "=")
			p.warn(n, "group variable %q in [%s:vars] not imported; move it to group_vars", safeKey(key), group)
			return nil
		}

		words, err := configtext.Words(line, true)
		if err != nil {
			return err
		}
		if len(words) == 0 {
			return nil
		}
		if section == "children" {
			if len(words) != 1 {
				return fmt.Errorf("expected one child group")
			}
			member := words[0]
			if group == "ungrouped" || member == "all" || (member == "ungrouped" && group != "all") {
				return fmt.Errorf("invalid relationship between special groups %q and %q", group, member)
			}
			if err := addGroup(member); err != nil {
				return err
			}
			if group != "all" {
				edge := store.Pair{First: group, Second: member}
				if seenEdges[edge] {
					p.warn(n, "duplicate child %q in %q", member, group)
				} else {
					p.children = append(p.children, edge)
					seenEdges[edge] = true
				}
			}
			return nil
		}

		if strings.ContainsAny(words[0], "[]") {
			return fmt.Errorf("Ansible host ranges and bracketed literals are unsupported; use an explicit canonical name")
		}
		name, port, hasPort := strings.Cut(words[0], ":")
		if err := store.CanonicalName(name); err != nil {
			return err
		}
		host := hosts[name]
		if host == nil {
			host = &record{names: []string{name}, line: n}
			hosts[name] = host
			p.hosts = append(p.hosts, host)
		}
		member := store.Pair{First: group, Second: name}
		if seenMembers[member] {
			p.warn(n, "duplicate host %q in group %q", name, group)
		} else if group != "" && group != "all" && group != "ungrouped" {
			p.memberships = append(p.memberships, member)
		}
		seenMembers[member] = true
		if hasPort {
			if err := setField(host, "port", port); err != nil {
				return err
			}
		}
		for _, word := range words[1:] {
			key, value, ok := strings.Cut(word, "=")
			if !ok {
				return fmt.Errorf("expected a host variable assignment")
			}
			switch key {
			case "ansible_host", "ansible_user", "ansible_port":
				if err := setField(host, key, value); err != nil {
					return err
				}
			case "ansible_ssh_host", "ansible_ssh_user", "ansible_ssh_port":
				if err := setField(host, strings.Replace(key, "ansible_ssh_", "ansible_", 1), value); err != nil {
					return err
				}
			default:
				p.warn(n, "host variable %q for %q not imported; move it to host_vars", safeKey(key), name)
			}
		}
		return nil
	})
	return p, err
}

func safeKey(key string) string {
	key = strings.TrimSpace(key)
	if strings.ContainsAny(key, " \t'\"") {
		return "(invalid variable name)"
	}
	return key
}
