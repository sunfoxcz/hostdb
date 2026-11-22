package importer

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"hostdb/internal/store"
)

type Options struct {
	Map                map[string]string
	AllowSharedAddress bool
}

func (p *Plan) Apply(ctx context.Context, q store.Queries, opts Options) ([]string, error) {
	var changes []string
	groups := make([]string, 0, len(p.groups))
	for group := range p.groups {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	existingGroups, err := q.Names(ctx, "groups")
	if err != nil {
		return nil, err
	}
	haveGroup := make(map[string]bool, len(existingGroups))
	for _, g := range existingGroups {
		haveGroup[g] = true
	}
	for _, group := range groups {
		if !haveGroup[group] {
			if err := q.AddGroup(ctx, group, ""); err != nil {
				return nil, err
			}
			changes = append(changes, fmt.Sprintf("add group %s", group))
		}
	}
	used := map[string]bool{}
	for _, host := range p.hosts {
		if err := p.applyHost(ctx, q, opts, used, host, &changes); err != nil {
			return nil, fmt.Errorf("line %d: %w", host.line, err)
		}
	}
	var unused []string
	for name := range opts.Map {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		sort.Strings(unused)
		return nil, fmt.Errorf("mapping refers to SSH names absent from the import: %s", strings.Join(unused, ", "))
	}

	edges, err := q.Children(ctx)
	if err != nil {
		return nil, err
	}
	if err := applyLinks(ctx, q, edges, p.children, true, &changes); err != nil {
		return nil, err
	}
	members, err := q.Memberships(ctx, false)
	if err != nil {
		return nil, err
	}
	if err := applyLinks(ctx, q, members, p.memberships, false, &changes); err != nil {
		return nil, err
	}
	return changes, nil
}

func applyLinks(ctx context.Context, q store.Queries, existing, incoming []store.Pair, child bool, changes *[]string) error {
	have := make(map[store.Pair]bool, len(existing))
	for _, pair := range existing {
		have[pair] = true
	}
	for _, pair := range incoming {
		if have[pair] {
			continue
		}
		if err := q.Link(ctx, pair.First, pair.Second, child, true); err != nil {
			return err
		}
		kind := "host"
		if child {
			kind = "child group"
		}
		*changes = append(*changes, fmt.Sprintf("add %s %s to %s", kind, pair.Second, pair.First))
		have[pair] = true
	}
	return nil
}

func (p *Plan) applyHost(ctx context.Context, q store.Queries, opts Options, used map[string]bool, r *record, changes *[]string) error {
	var canonical string
	for _, n := range r.names {
		if target, ok := opts.Map[n]; ok {
			used[n] = true
			if canonical != "" && target != canonical {
				return fmt.Errorf("conflicting canonical mappings for SSH block %q", r.names[0])
			}
			canonical = target
		}
	}
	for _, n := range r.names {
		h, found, err := q.HostBySSHName(ctx, n)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if (!p.SSH && h.Name != n) || (canonical != "" && h.Name != canonical) {
			return fmt.Errorf("name %q already belongs to canonical host %q; reconcile names before importing", n, h.Name)
		}
		canonical = h.Name
	}
	if canonical == "" {
		canonical = r.names[0]
	}
	h, exists, err := q.HostBySSHName(ctx, canonical)
	if err != nil {
		return err
	}
	if exists && h.Name != canonical {
		return fmt.Errorf("mapping target %q is an alias of %q; use the canonical name", canonical, h.Name)
	}
	address := r.address
	if address == "" && !exists {
		address = r.names[0]
	}
	if address != "" && (!exists || h.Hostname != address) && !opts.AllowSharedAddress {
		others, err := q.HostsAtAddress(ctx, address)
		if err != nil {
			return err
		}
		if len(others) > 0 {
			return fmt.Errorf("address %q is already used by %s; map the SSH name to the canonical host, or use --allow-shared-address for distinct hosts", address, strings.Join(others, ", "))
		}
	}
	if !exists {
		port := r.port
		if port == 0 {
			port = 22
		}
		h = store.Host{Name: canonical, Hostname: address, User: r.user, Port: port, Enabled: true}
		h.ID, err = q.CreateHost(ctx, h)
		if err != nil {
			return err
		}
		*changes = append(*changes, fmt.Sprintf("add host %s (%s, port %d)", canonical, address, port))
	} else {
		fields := []struct {
			name, before, after string
			update, empty       bool
		}{
			{"hostname", h.Hostname, r.address, r.address != "", h.Hostname == h.Name},
			{"ssh_user", h.User, r.user, r.hasUser, h.User == ""},
			{"ssh_port", strconv.Itoa(h.Port), strconv.Itoa(r.port), r.port != 0, h.Port == 22},
		}
		for _, f := range fields {
			if !f.update || f.before == f.after {
				continue
			}
			if !f.empty {
				return fmt.Errorf("host %q has conflicting %s (%q vs %q)", canonical, f.name, f.before, f.after)
			}
			if err := q.SetHost(ctx, canonical, f.name, f.after); err != nil {
				return err
			}
			*changes = append(*changes, fmt.Sprintf("set %s %s to %q", canonical, f.name, f.after))
		}
	}
	for _, n := range r.names {
		if n == canonical {
			continue
		}
		_, found, err := q.HostBySSHName(ctx, n)
		if err != nil {
			return err
		}
		if !found {
			if err := q.Alias(ctx, h.ID, n, true); err != nil {
				return err
			}
			*changes = append(*changes, fmt.Sprintf("add SSH alias %s to %s", n, canonical))
		}
	}
	return mergeOptions(ctx, q, h, r.options, changes)
}

type optionValue struct{ name, value string }

func mergeOptions(ctx context.Context, q store.Queries, h store.Host, incoming []store.Option, changes *[]string) error {
	if len(incoming) == 0 {
		return nil
	}
	existing, err := q.Options(ctx, h.ID)
	if err != nil {
		return err
	}
	hasIncoming := map[string]bool{}
	hasExisting := map[string]bool{}
	for _, opt := range incoming {
		hasIncoming[strings.ToLower(opt.Name)] = true
	}
	var overlappingExisting, overlappingIncoming []optionValue
	for _, opt := range existing {
		name := strings.ToLower(opt.Name)
		hasExisting[name] = true
		if hasIncoming[name] {
			overlappingExisting = append(overlappingExisting, optionValue{name, opt.Value})
		}
	}
	var additions []store.Option
	for _, opt := range incoming {
		name := strings.ToLower(opt.Name)
		if hasExisting[name] {
			if len(additions) != 0 {
				return fmt.Errorf("SSH directives for %q would require reordering existing values", h.Name)
			}
			overlappingIncoming = append(overlappingIncoming, optionValue{name, opt.Value})
		} else {
			additions = append(additions, opt)
		}
	}
	if !slices.Equal(overlappingExisting, overlappingIncoming) {
		return fmt.Errorf("SSH directives for %q conflict with existing values or their order", h.Name)
	}
	for _, opt := range additions {
		if err := q.Option(ctx, h.ID, "add", opt.Name, &opt.Value); err != nil {
			return err
		}
		*changes = append(*changes, fmt.Sprintf("add SSH directive %s to %s", opt.Name, h.Name))
	}
	return nil
}
