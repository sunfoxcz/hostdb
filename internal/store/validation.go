package store

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"hostdb/internal/configtext"
)

var (
	hostPattern    = regexp.MustCompile("^[a-z0-9_][a-z0-9_.-]*$")
	addressPattern = regexp.MustCompile("^[A-Za-z0-9_][A-Za-z0-9_.-]*$")
	zonePattern    = regexp.MustCompile("^[A-Za-z0-9_.-]+$")
	userPattern    = regexp.MustCompile("^[A-Za-z0-9_][A-Za-z0-9_.@-]*[$]?$")
	groupPattern   = regexp.MustCompile("^[A-Za-z0-9_][A-Za-z0-9_.-]*$")
	optionPattern  = regexp.MustCompile("^[A-Za-z][A-Za-z0-9]*$")
)

func HostName(name string) error {
	if !hostPattern.MatchString(name) || len(name) > 253 {
		return fmt.Errorf("invalid canonical host name or alias %q (use lowercase letters, digits, underscores, dots or hyphens)", name)
	}
	return nil
}

func CanonicalName(name string) error {
	if name == "all" || name == "ungrouped" || name == "_meta" {
		return fmt.Errorf("reserved Ansible host name %q", name)
	}
	return HostName(name)
}

func Address(value string) error {
	if addr, err := netip.ParseAddr(value); err == nil && (addr.Zone() == "" || zonePattern.MatchString(addr.Zone())) {
		return nil
	}
	if len(value) > 253 || !addressPattern.MatchString(value) {
		return fmt.Errorf("invalid hostname or IP address %q", value)
	}
	return nil
}

func User(value string) error {
	if value != "" && !userPattern.MatchString(value) {
		return fmt.Errorf("invalid SSH user %q", value)
	}
	return nil
}

func Port(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid SSH port %d (expected 1–65535)", port)
	}
	return nil
}

func GroupName(name string) error {
	if len(name) > 253 || !groupPattern.MatchString(name) || name == "all" || name == "ungrouped" || name == "_meta" {
		return fmt.Errorf("invalid or reserved Ansible group name %q", name)
	}
	return nil
}

func Description(value string) error {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("description must be valid single-line text")
	}
	return nil
}

func OptionName(name string) error {
	if !optionPattern.MatchString(name) {
		return fmt.Errorf("invalid SSH directive name %q", name)
	}
	switch strings.ToLower(name) {
	case "host", "match", "include", "ignoreunknown", "password", "passphrase", "privatekey":
		return fmt.Errorf("SSH directive %s is not allowed in a host block", name)
	case "hostname", "user", "port":
		return fmt.Errorf("use host settings for SSH directive %s", name)
	}
	return nil
}

func OptionValue(name, value string) error {
	if text := strings.TrimSpace(value); text == "" || strings.HasPrefix(text, "#") || !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t' || r == '\u2028' || r == '\u2029'
	}) {
		return fmt.Errorf("SSH directive value must be nonempty single-line text")
	}
	switch strings.ToLower(name) {
	case "proxycommand", "remotecommand", "localcommand", "knownhostscommand":
		// OpenSSH passes these arguments through as a command line.
		return nil
	}
	words, err := configtext.Words(value, false)
	if err != nil {
		return err
	}
	if len(words) == 0 || words[0] == "" {
		return fmt.Errorf("SSH directive requires an argument")
	}
	switch strings.ToLower(name) {
	case "passwordauthentication", "identitiesonly":
		word := strings.ToLower(words[0])
		if len(words) != 1 || (word != "yes" && word != "no" && word != "true" && word != "false") {
			return fmt.Errorf("expected yes, no, true or false")
		}
	case "serveralivecountmax":
		count, err := strconv.Atoi(words[0])
		if err != nil || count < 0 || len(words) != 1 {
			return fmt.Errorf("expected a nonnegative integer")
		}
	case "kexalgorithms", "hostkeyalgorithms", "macs", "ciphers", "pubkeyacceptedalgorithms", "hostbasedacceptedalgorithms":
		if len(words) != 1 || strings.TrimLeft(words[0], "+-^") == "" {
			return fmt.Errorf("expected one algorithm list")
		}
	}
	return nil
}
