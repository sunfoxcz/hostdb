package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Group struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Hosts       []string `json:"hosts,omitempty"`
	Children    []string `json:"children,omitempty"`
	Parents     []string `json:"parents,omitempty"`
}

type Pair struct{ First, Second string }

func scanGroup(row scanner) (g Group, err error) {
	err = row.Scan(&g.ID, &g.Name, &g.Description)
	return
}

func (q Queries) Group(ctx context.Context, name string) (Group, error) {
	g, err := scanGroup(q.db.QueryRowContext(ctx, "SELECT id,name,description FROM groups WHERE name=?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return g, fmt.Errorf("group %q not found", name)
	}
	return g, err
}

func (q Queries) GroupDetails(ctx context.Context, name string) (Group, error) {
	g, err := q.Group(ctx, name)
	if err != nil {
		return g, err
	}
	g.Hosts, err = collect(ctx, q.db, "SELECT h.name FROM hosts h JOIN host_groups hg ON hg.host_id=h.id WHERE hg.group_id=? ORDER BY h.name", scanString, g.ID)
	if err != nil {
		return g, err
	}
	g.Children, err = collect(ctx, q.db, "SELECT g.name FROM groups g JOIN group_children gc ON gc.child_id=g.id WHERE gc.parent_id=? ORDER BY g.name", scanString, g.ID)
	if err != nil {
		return g, err
	}
	g.Parents, err = collect(ctx, q.db, "SELECT g.name FROM groups g JOIN group_children gc ON gc.parent_id=g.id WHERE gc.child_id=? ORDER BY g.name", scanString, g.ID)
	return g, err
}

func (q Queries) Groups(ctx context.Context) ([]Group, error) {
	return collect(ctx, q.db, "SELECT id,name,description FROM groups ORDER BY name", func(r *sql.Rows) (Group, error) {
		return scanGroup(r)
	})
}

func (q Queries) AddGroup(ctx context.Context, name, description string) error {
	if err := GroupName(name); err != nil {
		return err
	}
	if err := Description(description); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, "INSERT INTO groups(name,description) VALUES (?,?)", name, description)
	return err
}

func (q Queries) DescribeGroup(ctx context.Context, name, description string) error {
	if err := Description(description); err != nil {
		return err
	}
	r, err := q.db.ExecContext(ctx, "UPDATE groups SET description=? WHERE name=?", description, name)
	return changed(r, err, fmt.Sprintf("group %q", name))
}

func (q Queries) RemoveGroup(ctx context.Context, name string) error {
	r, err := q.db.ExecContext(ctx, "DELETE FROM groups WHERE name=?", name)
	return changed(r, err, fmt.Sprintf("group %q", name))
}

func (q Queries) Link(ctx context.Context, group, member string, child, add bool) error {
	first, err := q.Group(ctx, group)
	if err != nil {
		return err
	}
	var secondID int64
	var table, firstKey, secondKey string
	if child {
		g, err := q.Group(ctx, member)
		if err != nil {
			return err
		}
		secondID, table, firstKey, secondKey = g.ID, "group_children", "parent_id", "child_id"
	} else {
		h, err := q.Host(ctx, member)
		if err != nil {
			return err
		}
		secondID, table, firstKey, secondKey = h.ID, "host_groups", "group_id", "host_id"
	}
	if add {
		query := "INSERT INTO " + table + "(" + firstKey + "," + secondKey + ") VALUES (?,?)"
		_, err := q.db.ExecContext(ctx, query, first.ID, secondID)
		if err != nil {
			return fmt.Errorf("link %q to %q: %w", member, group, err)
		}
		return nil
	}
	r, err := q.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+firstKey+"=? AND "+secondKey+"=?", first.ID, secondID)
	return changed(r, err, fmt.Sprintf("relationship %q → %q", group, member))
}

func scanPair(r *sql.Rows) (p Pair, err error) {
	err = r.Scan(&p.First, &p.Second)
	return
}

func (q Queries) Children(ctx context.Context) ([]Pair, error) {
	return collect(ctx, q.db, "SELECT p.name,c.name FROM group_children gc JOIN groups p ON p.id=gc.parent_id JOIN groups c ON c.id=gc.child_id ORDER BY p.name,c.name", scanPair)
}

func (q Queries) Memberships(ctx context.Context, enabledOnly bool) ([]Pair, error) {
	query := "SELECT g.name,h.name FROM host_groups hg JOIN groups g ON g.id=hg.group_id JOIN hosts h ON h.id=hg.host_id"
	if enabledOnly {
		query += " WHERE h.enabled=1"
	}
	return collect(ctx, q.db, query+" ORDER BY g.name,h.name", scanPair)
}
