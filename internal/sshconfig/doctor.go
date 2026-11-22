package sshconfig

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"hostdb/internal/configtext"
	"hostdb/internal/store"
)

func Check(ctx context.Context, out io.Writer, ssh, manual, generated string, hosts []store.Host) error {
	want, err := Generate(hosts)
	if err != nil {
		return err
	}
	var issues []error
	info, err := os.Lstat(generated)
	switch {
	case err != nil:
		issues = append(issues, fmt.Errorf("generated SSH config %q: %w (run hostdb ssh sync)", generated, err))
	case !info.Mode().IsRegular():
		issues = append(issues, fmt.Errorf("generated SSH config %q is not a regular file", generated))
	default:
		if info.Mode().Perm()&0077 != 0 {
			issues = append(issues, fmt.Errorf("generated SSH config %q is accessible to other users", generated))
		}
		current, err := readConfig(generated)
		if err != nil {
			issues = append(issues, err)
		} else if !bytes.Equal(current, want) {
			issues = append(issues, fmt.Errorf("generated SSH config %q is stale (run hostdb ssh sync)", generated))
		} else {
			fmt.Fprintln(out, "Generated SSH config is current:", generated)
		}
	}
	if ssh == "" {
		return errors.Join(issues...)
	}

	dir, err := os.MkdirTemp("", "hostdb-doctor-*")
	if err != nil {
		return errors.Join(append(issues, err)...)
	}
	defer os.RemoveAll(dir)
	snapshot := configSnapshot{
		dir: dir, base: filepath.Dir(manual), generated: generated,
		copies: map[string]string{}, visiting: map[string]bool{},
	}
	userConfig, err := snapshot.copy(manual)
	if err != nil {
		issues = append(issues, fmt.Errorf("cannot safely evaluate user SSH config %q: %w", manual, err))
	} else if !snapshot.included {
		issues = append(issues, fmt.Errorf("no Include for %q found in %q", generated, manual))
	} else {
		fmt.Fprintln(out, "Found Include for generated SSH config in:", manual)
	}

	baseline := filepath.Join(dir, "host.conf")
	checked := 0
	for _, host := range hosts {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(issues, err)...)
		}
		config, err := Generate([]store.Host{host})
		if err != nil {
			return errors.Join(append(issues, err)...)
		}
		if err := os.WriteFile(baseline, config, 0600); err != nil {
			return errors.Join(append(issues, err)...)
		}
		expected, err := effective(ctx, ssh, baseline, host.Name)
		if err != nil {
			issues = append(issues, fmt.Errorf("OpenSSH rejected settings for %q: %w", host.Name, err))
			continue
		}
		if userConfig == "" || !snapshot.included {
			continue
		}
		names := append([]string{host.Name}, host.Aliases...)
		for _, name := range names {
			actual, err := effective(ctx, ssh, userConfig, name)
			if err != nil {
				issues = append(issues, fmt.Errorf("OpenSSH could not evaluate %q: %w", name, err))
				// Other names may hit different blocks, but a parse error can
				// otherwise produce thousands of identical diagnostics.
				userConfig = ""
				break
			}
			if err := compare(host, expected, actual); err != nil {
				issues = append(issues, fmt.Errorf("SSH name %q resolves with overrides or a missing Include: %w", name, err))
			} else {
				checked++
			}
		}
	}
	if checked > 0 {
		fmt.Fprintf(out, "OpenSSH resolved %d canonical names and aliases with the configured settings.\n", checked)
	}
	return errors.Join(issues...)
}

func effective(ctx context.Context, ssh, config, name string) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ssh, "-G", "-T", "-F", config, "-o", "CanonicalizeHostname=no", "--", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	result := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			result[key] = append(result[key], value)
		}
	}
	return result, nil
}

func compare(h store.Host, expected, actual map[string][]string) error {
	keys := []string{"hostname", "port"}
	if h.User != "" {
		keys = append(keys, "user")
	}
	seen := map[string]bool{}
	for _, opt := range h.Options {
		key := strings.ToLower(opt.Name)
		switch key {
		case "pubkeyacceptedkeytypes":
			key = "pubkeyacceptedalgorithms"
		case "hostbasedkeytypes":
			key = "hostbasedacceptedalgorithms"
		case "challengeresponseauthentication":
			key = "kbdinteractiveauthentication"
		case "proxycommand", "proxyjump":
			if !seen["proxycommand"] && !seen["proxyjump"] {
				keys = append(keys, "proxycommand", "proxyjump")
				seen["proxycommand"], seen["proxyjump"] = true, true
			}
			continue
		case "canonicalizehostname":
			// A command-line override prevents DNS lookups during diagnostics.
			continue
		}
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}

	var issues []error
	for _, key := range keys {
		want, got := expected[key], actual[key]
		match := slices.Equal(want, got)
		if key == "identityfile" || key == "certificatefile" {
			remaining := want
			for _, value := range got {
				if len(remaining) != 0 && remaining[0] == value {
					remaining = remaining[1:]
				}
			}
			match = len(remaining) == 0
		}
		if !match {
			// Option values can include user-provided arguments. Don't echo them.
			issues = append(issues, fmt.Errorf("effective %s differs from the generated config", key))
		}
	}
	return errors.Join(issues...)
}

// Snapshot the user's Include tree with rewritten paths. Match conditions are
// deliberately refused because OpenSSH can execute their commands even with -G.
type configSnapshot struct {
	dir, base, generated string
	copies               map[string]string
	visiting             map[string]bool
	included             bool
}

var configKeyword = regexp.MustCompile("^[A-Za-z][A-Za-z0-9]*$")

func readConfig(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("SSH configuration %q must be a regular file", path)
	}
	const limit = 16 * 1024 * 1024
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && len(data) > limit {
		err = fmt.Errorf("SSH configuration %q exceeds %d bytes", path, limit)
	}
	return data, err
}

func (s *configSnapshot) copy(path string) (string, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if s.visiting[path] {
		return "", fmt.Errorf("recursive SSH Include of %q", path)
	}
	if target, ok := s.copies[path]; ok {
		return target, nil
	}
	if len(s.copies) >= 256 || len(s.visiting) >= 16 {
		return "", errors.New("SSH Include tree exceeds the diagnostic limit")
	}
	s.visiting[path] = true
	defer delete(s.visiting, path)
	target := filepath.Join(s.dir, fmt.Sprintf("user-%d.conf", len(s.copies)))
	s.copies[path] = target
	data, err := readConfig(path)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), 1024*1024)
	for scan.Scan() {
		line := scan.Text()
		// Reject ambiguous input before handing it to a different parser.
		if strings.ContainsAny(line, "\x00\r\v\f") {
			return "", fmt.Errorf("unexpected control character in %q", path)
		}
		key, value := Directive(line)
		if key != "" && !configKeyword.MatchString(key) {
			return "", fmt.Errorf("cannot safely interpret an SSH keyword in %q", path)
		}
		switch strings.ToLower(key) {
		case "match":
			return "", fmt.Errorf("Match section in %q can execute commands during ssh -G", path)
		case "include":
			words, err := configtext.Words(value, false)
			if err != nil {
				return "", err
			}
			if len(words) == 0 {
				return "", fmt.Errorf("empty Include in %q", path)
			}
			for _, pattern := range words {
				if strings.HasPrefix(pattern, "~/") {
					pattern = filepath.Join(filepath.Dir(s.base), pattern[2:])
				} else if strings.ContainsAny(pattern, "%$") || strings.HasPrefix(pattern, "~") {
					return "", fmt.Errorf("cannot inspect Include expansion %q", pattern)
				} else if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(s.base, pattern)
				}
				matches, err := filepath.Glob(pattern)
				if err != nil {
					return "", err
				}
				for _, name := range matches {
					if name == s.generated {
						s.included = true
					}
					replacement, err := s.copy(name)
					if err != nil {
						return "", err
					}
					if strings.ContainsAny(replacement, "\r\n\\\"%$") {
						return "", errors.New("temporary path cannot be represented as a literal SSH Include")
					}
					fmt.Fprintf(&out, "Include \"%s\"\n", replacement)
				}
			}
		default:
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if err := scan.Err(); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(out.String()), 0600); err != nil {
		return "", err
	}
	return target, nil
}
