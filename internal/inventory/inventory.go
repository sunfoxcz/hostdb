package inventory

import (
	"context"

	"hostdb/internal/store"
)

type Group struct {
	Hosts    []string `json:"hosts"`
	Children []string `json:"children,omitempty"`
}

type Vars map[string]any

type metadata struct {
	HostVars map[string]Vars `json:"hostvars"`
}

func List(ctx context.Context, q store.Queries) (map[string]any, error) {
	names, err := q.Names(ctx, "hosts")
	if err != nil {
		return nil, err
	}
	groups, err := q.Names(ctx, "groups")
	if err != nil {
		return nil, err
	}
	edges, err := q.Children(ctx)
	if err != nil {
		return nil, err
	}
	members, err := q.Memberships(ctx, true)
	if err != nil {
		return nil, err
	}

	hostvars := make(map[string]Vars, len(names))
	grouped := make(map[string]bool, len(members))
	nested := make(map[string]bool, len(edges))
	result := make(map[string]any, len(groups)+3)
	result["_meta"] = metadata{HostVars: hostvars}
	for _, g := range groups {
		// Ansible's script plugin can interpret {} as a host named after
		// the group. The explicit empty array identifies group data.
		result[g] = &Group{Hosts: []string{}}
	}
	for _, edge := range edges {
		g := result[edge.First].(*Group)
		g.Children = append(g.Children, edge.Second)
		nested[edge.Second] = true
	}
	for _, member := range members {
		g := result[member.First].(*Group)
		g.Hosts = append(g.Hosts, member.Second)
		grouped[member.Second] = true
	}
	all := &Group{Hosts: []string{}, Children: []string{"ungrouped"}}
	ungrouped := &Group{Hosts: []string{}}
	for _, g := range groups {
		if !nested[g] {
			all.Children = append(all.Children, g)
		}
	}
	for _, name := range names {
		hostvars[name] = Vars{}
		if !grouped[name] {
			ungrouped.Hosts = append(ungrouped.Hosts, name)
		}
	}
	result["all"] = all
	result["ungrouped"] = ungrouped
	return result, nil
}
