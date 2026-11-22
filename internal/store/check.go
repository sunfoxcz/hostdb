package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (q Queries) Validate(ctx context.Context) error {
	var issues []error
	checks, err := collect(ctx, q.db, "PRAGMA integrity_check", scanString)
	if err != nil {
		return err
	}
	for _, check := range checks {
		if check != "ok" {
			issues = append(issues, fmt.Errorf("SQLite integrity: %s", check))
		}
	}
	fks, err := collect(ctx, q.db, "PRAGMA foreign_key_check", func(r *sql.Rows) (message string, err error) {
		var table, parent string
		var row sql.NullInt64
		var id int
		err = r.Scan(&table, &row, &parent, &id)
		return fmt.Sprintf("broken foreign key %d in %s referencing %s", id, table, parent), err
	})
	if err != nil {
		return err
	}
	for _, fk := range fks {
		issues = append(issues, errors.New(fk))
	}
	hosts, err := q.SSHHosts(ctx, false)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, h := range hosts {
		if err := ValidateHost(h); err != nil {
			issues = append(issues, fmt.Errorf("host %q: %w", h.Name, err))
		}
		if previous, exists := names[h.Name]; exists {
			issues = append(issues, fmt.Errorf("host name %q collides with SSH name on %q", h.Name, previous))
		}
		names[h.Name] = h.Name
		for _, a := range h.Aliases {
			if err := HostName(a); err != nil {
				issues = append(issues, err)
			}
			if previous, exists := names[a]; exists {
				issues = append(issues, fmt.Errorf("SSH alias %q collides with SSH name on %q", a, previous))
			}
			names[a] = h.Name
		}
		for _, o := range h.Options {
			if err := errors.Join(OptionName(o.Name), OptionValue(o.Name, o.Value)); err != nil {
				issues = append(issues, fmt.Errorf("host %q, option %s: %w", h.Name, o.Name, err))
			}
		}
	}
	groups, err := q.Groups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if err := errors.Join(GroupName(g.Name), Description(g.Description)); err != nil {
			issues = append(issues, fmt.Errorf("group %q: %w", g.Name, err))
		}
	}
	edges, err := q.Children(ctx)
	if err != nil {
		return err
	}
	children := make(map[string][]string, len(groups))
	indegree := make(map[string]int, len(groups))
	for _, edge := range edges {
		children[edge.First] = append(children[edge.First], edge.Second)
		indegree[edge.Second]++
	}
	queue := make([]string, 0, len(groups))
	for _, g := range groups {
		if indegree[g.Name] == 0 {
			queue = append(queue, g.Name)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, next := range children[queue[i]] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if len(queue) != len(groups) {
		issues = append(issues, errors.New("cycle in group relationships"))
	}
	return errors.Join(issues...)
}
