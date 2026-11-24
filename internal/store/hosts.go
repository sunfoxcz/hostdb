package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

type Host struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Hostname    string   `json:"hostname"`
	User        string   `json:"ssh_user"`
	Port        int      `json:"ssh_port"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	Aliases     []string `json:"ssh_aliases,omitempty"`
	Options     []Option `json:"ssh_options,omitempty"`
	Groups      []string `json:"groups,omitempty"`
}

type Option struct {
	Name     string `json:"option_name"`
	Value    string `json:"option_value"`
	Position int    `json:"position"`
}

type scanner interface{ Scan(...any) error }

func scanHost(row scanner) (h Host, err error) {
	err = row.Scan(&h.ID, &h.Name, &h.Hostname, &h.User, &h.Port, &h.Description, &h.Enabled, &h.CreatedAt, &h.UpdatedAt)
	return
}

const hostColumns = "id, name, hostname, ssh_user, ssh_port, description, enabled, created_at, updated_at"

func (q Queries) Host(ctx context.Context, name string) (Host, error) {
	h, err := scanHost(q.db.QueryRowContext(ctx, "SELECT "+hostColumns+" FROM hosts WHERE name=?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return h, fmt.Errorf("host %q not found", name)
	}
	return h, err
}

func (q Queries) HostBySSHName(ctx context.Context, name string) (h Host, found bool, err error) {
	h, err = scanHost(q.db.QueryRowContext(ctx, "SELECT "+hostColumns+" FROM hosts WHERE name=? OR id=(SELECT host_id FROM ssh_aliases WHERE alias=?)", name, name))
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	return h, err == nil, err
}

func (q Queries) Hosts(ctx context.Context, group string) ([]Host, error) {
	query, args, err := q.hostListQuery(ctx, hostColumns, group)
	if err != nil {
		return nil, err
	}
	return collect(ctx, q.db, query, func(r *sql.Rows) (Host, error) {
		return scanHost(r)
	}, args...)
}

func (q Queries) HostNames(ctx context.Context, group string) ([]string, error) {
	query, args, err := q.hostListQuery(ctx, "name", group)
	if err != nil {
		return nil, err
	}
	return collect(ctx, q.db, query, scanString, args...)
}

func (q Queries) hostListQuery(ctx context.Context, columns, group string) (string, []any, error) {
	query := "SELECT " + columns + " FROM hosts"
	var args []any
	switch group {
	case "", "all":
	case "ungrouped":
		query += " WHERE NOT EXISTS (SELECT 1 FROM host_groups WHERE host_id=hosts.id)"
	default:
		g, err := q.Group(ctx, group)
		if err != nil {
			return "", nil, err
		}
		query += ` WHERE id IN (
			WITH RECURSIVE descendants(id) AS (
				VALUES (?)
				UNION
				SELECT gc.child_id
				FROM group_children gc JOIN descendants d ON gc.parent_id=d.id
			)
			SELECT hg.host_id FROM descendants d JOIN host_groups hg ON hg.group_id=d.id
		)`
		args = []any{g.ID}
	}
	return query + " ORDER BY name", args, nil
}

func (q Queries) HostDetails(ctx context.Context, name string) (Host, error) {
	h, err := q.Host(ctx, name)
	if err != nil {
		return h, err
	}
	h.Aliases, err = collect(ctx, q.db, "SELECT alias FROM ssh_aliases WHERE host_id=? ORDER BY alias", scanString, h.ID)
	if err != nil {
		return h, err
	}
	h.Options, err = q.Options(ctx, h.ID)
	if err != nil {
		return h, err
	}
	h.Groups, err = collect(ctx, q.db, "SELECT g.name FROM groups g JOIN host_groups hg ON hg.group_id=g.id WHERE hg.host_id=? ORDER BY g.name", scanString, h.ID)
	return h, err
}

func (q Queries) CreateHost(ctx context.Context, h Host) (int64, error) {
	if err := ValidateHost(h); err != nil {
		return 0, err
	}
	r, err := q.db.ExecContext(ctx, "INSERT INTO hosts(name,hostname,ssh_user,ssh_port,description,enabled) VALUES (?,?,?,?,?,?)",
		h.Name, h.Hostname, h.User, h.Port, h.Description, h.Enabled)
	if err != nil {
		return 0, fmt.Errorf("add host %q: %w", h.Name, err)
	}
	return r.LastInsertId()
}

func ValidateHost(h Host) error {
	for _, err := range []error{CanonicalName(h.Name), Address(h.Hostname), User(h.User), Port(h.Port), Description(h.Description)} {
		if err != nil {
			return err
		}
	}
	return nil
}

const touch = "updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')"

func (q Queries) SetHost(ctx context.Context, name, field, value string) error {
	var column string
	var arg any = value
	var err error
	switch field {
	case "name":
		column, err = "name", CanonicalName(value)
	case "hostname":
		column, err = "hostname", Address(value)
	case "user", "ssh_user":
		column, err = "ssh_user", User(value)
	case "port", "ssh_port":
		var port int
		port, err = strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid SSH port %q", value)
		}
		column, arg, err = "ssh_port", port, Port(port)
	case "description":
		column, err = "description", Description(value)
	case "enabled":
		column = "enabled"
		arg, err = strconv.ParseBool(value)
	default:
		return fmt.Errorf("unknown host field %q", field)
	}
	if err != nil {
		return err
	}
	r, err := q.db.ExecContext(ctx, "UPDATE hosts SET "+column+"=?, "+touch+" WHERE name=?", arg, name)
	if err != nil && field == "name" {
		return fmt.Errorf("rename host %q to %q: %w", name, value, err)
	}
	return changed(r, err, fmt.Sprintf("host %q", name))
}

func (q Queries) RemoveHost(ctx context.Context, name string) error {
	r, err := q.db.ExecContext(ctx, "DELETE FROM hosts WHERE name=?", name)
	return changed(r, err, fmt.Sprintf("host %q", name))
}

func (q Queries) Alias(ctx context.Context, id int64, alias string, add bool) error {
	if err := HostName(alias); err != nil {
		return err
	}
	if add {
		_, err := q.db.ExecContext(ctx, "INSERT INTO ssh_aliases(host_id,alias) VALUES (?,?)", id, alias)
		if err != nil {
			return fmt.Errorf("add SSH alias %q: %w", alias, err)
		}
	} else {
		r, err := q.db.ExecContext(ctx, "DELETE FROM ssh_aliases WHERE host_id=? AND alias=?", id, alias)
		if err := changed(r, err, fmt.Sprintf("SSH alias %q on host", alias)); err != nil {
			return err
		}
	}
	return q.touchHost(ctx, id)
}

func (q Queries) touchHost(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, "UPDATE hosts SET "+touch+" WHERE id=?", id)
	return err
}

func (q Queries) Options(ctx context.Context, id int64) ([]Option, error) {
	return collect(ctx, q.db, "SELECT option_name,option_value,position FROM ssh_options WHERE host_id=? ORDER BY position", func(r *sql.Rows) (o Option, err error) {
		err = r.Scan(&o.Name, &o.Value, &o.Position)
		return
	}, id)
}

func (q Queries) Option(ctx context.Context, id int64, action, name string, value *string) error {
	if err := OptionName(name); err != nil {
		return err
	}
	if value != nil {
		if err := OptionValue(name, *value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	switch action {
	case "set", "add":
		if value == nil {
			return errors.New("SSH directive value is required")
		}
		var position int
		query := "SELECT coalesce(max(position)+1,0) FROM ssh_options WHERE host_id=?"
		args := []any{id}
		if action == "set" {
			query = "SELECT coalesce(min(CASE WHEN option_name=? THEN position END),max(position)+1,0) FROM ssh_options WHERE host_id=?"
			args = []any{name, id}
		}
		if err := q.db.QueryRowContext(ctx, query, args...).Scan(&position); err != nil {
			return err
		}
		if action == "set" {
			if _, err := q.db.ExecContext(ctx, "DELETE FROM ssh_options WHERE host_id=? AND option_name=?", id, name); err != nil {
				return err
			}
		}
		if _, err := q.db.ExecContext(ctx, "INSERT INTO ssh_options(host_id,option_name,option_value,position) VALUES (?,?,?,?)", id, name, *value, position); err != nil {
			return err
		}
	case "remove":
		query := "DELETE FROM ssh_options WHERE host_id=? AND option_name=?"
		args := []any{id, name}
		if value != nil {
			query += " AND option_value=?"
			args = append(args, *value)
		}
		r, err := q.db.ExecContext(ctx, query, args...)
		if err := changed(r, err, fmt.Sprintf("SSH directive %q on host", name)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown option action %q", action)
	}
	return q.touchHost(ctx, id)
}

func scanString(row *sql.Rows) (v string, err error) {
	err = row.Scan(&v)
	return
}

func (q Queries) Names(ctx context.Context, kind string) ([]string, error) {
	var query string
	switch kind {
	case "all-hosts":
		return q.HostNames(ctx, "")
	case "hosts":
		query = "SELECT name FROM hosts WHERE enabled=1 ORDER BY name"
	case "groups":
		query = "SELECT name FROM groups ORDER BY name"
	case "inventory-groups":
		query = "SELECT name FROM groups UNION SELECT 'all' UNION SELECT 'ungrouped' ORDER BY name"
	case "targets":
		query = "SELECT name FROM hosts WHERE enabled=1 UNION SELECT name FROM groups UNION SELECT 'all' UNION SELECT 'ungrouped' ORDER BY name"
	case "ssh-aliases":
		query = "SELECT a.alias FROM ssh_aliases a JOIN hosts h ON h.id=a.host_id WHERE h.enabled=1 ORDER BY a.alias"
	default:
		return nil, fmt.Errorf("unknown name list %q", kind)
	}
	return collect(ctx, q.db, query, scanString)
}

func (q Queries) SSHHosts(ctx context.Context, enabledOnly bool) ([]Host, error) {
	where := ""
	if enabledOnly {
		where = " WHERE enabled=1"
	}
	hosts, err := collect(ctx, q.db, "SELECT "+hostColumns+" FROM hosts"+where+" ORDER BY name", func(r *sql.Rows) (Host, error) {
		return scanHost(r)
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Host, len(hosts))
	for i := range hosts {
		byID[hosts[i].ID] = &hosts[i]
	}
	aliases, err := q.db.QueryContext(ctx, "SELECT host_id,alias FROM ssh_aliases ORDER BY host_id,alias")
	if err != nil {
		return nil, err
	}
	defer aliases.Close()
	for aliases.Next() {
		var id int64
		var alias string
		if err := aliases.Scan(&id, &alias); err != nil {
			return nil, err
		}
		if h := byID[id]; h != nil {
			h.Aliases = append(h.Aliases, alias)
		}
	}
	if err := aliases.Err(); err != nil {
		return nil, err
	}
	opts, err := q.db.QueryContext(ctx, "SELECT host_id,option_name,option_value,position FROM ssh_options ORDER BY host_id,position")
	if err != nil {
		return nil, err
	}
	defer opts.Close()
	for opts.Next() {
		var id int64
		var o Option
		if err := opts.Scan(&id, &o.Name, &o.Value, &o.Position); err != nil {
			return nil, err
		}
		if h := byID[id]; h != nil {
			h.Options = append(h.Options, o)
		}
	}
	return hosts, opts.Err()
}

func (q Queries) HostsAtAddress(ctx context.Context, address string) ([]string, error) {
	return collect(ctx, q.db, "SELECT name FROM hosts WHERE hostname=? COLLATE NOCASE ORDER BY name", scanString, address)
}
