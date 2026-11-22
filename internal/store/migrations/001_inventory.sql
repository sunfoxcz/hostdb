CREATE TABLE hosts (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    hostname TEXT NOT NULL,
    ssh_user TEXT NOT NULL DEFAULT '',
    ssh_port INTEGER NOT NULL DEFAULT 22 CHECK (ssh_port BETWEEN 1 AND 65535),
    description TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE TABLE ssh_aliases (
    id INTEGER PRIMARY KEY,
    host_id INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    alias TEXT NOT NULL UNIQUE
);
CREATE INDEX ssh_aliases_host ON ssh_aliases(host_id, alias);
CREATE TABLE ssh_options (
    id INTEGER PRIMARY KEY,
    host_id INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    option_name TEXT NOT NULL COLLATE NOCASE,
    option_value TEXT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    UNIQUE (host_id, position)
);
CREATE INDEX ssh_options_name ON ssh_options(host_id, option_name);

CREATE TABLE groups (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE host_groups (
    host_id INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (host_id, group_id)
) WITHOUT ROWID;
CREATE INDEX host_groups_group ON host_groups(group_id, host_id);
CREATE TABLE group_children (
    parent_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    child_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    PRIMARY KEY (parent_id, child_id),
    CHECK (parent_id != child_id)
) WITHOUT ROWID;
CREATE INDEX group_children_child ON group_children(child_id, parent_id);

CREATE TRIGGER host_name_insert BEFORE INSERT ON hosts
WHEN EXISTS (SELECT 1 FROM ssh_aliases WHERE alias = NEW.name)
BEGIN
    SELECT RAISE(ABORT, 'host name already used as SSH alias');
END;
CREATE TRIGGER host_name_update BEFORE UPDATE OF name ON hosts
WHEN EXISTS (SELECT 1 FROM ssh_aliases WHERE alias = NEW.name)
BEGIN
    SELECT RAISE(ABORT, 'host name already used as SSH alias');
END;
CREATE TRIGGER ssh_alias_insert BEFORE INSERT ON ssh_aliases
WHEN EXISTS (SELECT 1 FROM hosts WHERE name = NEW.alias)
BEGIN
    SELECT RAISE(ABORT, 'SSH alias already used as canonical host name');
END;
CREATE TRIGGER ssh_alias_update BEFORE UPDATE OF alias ON ssh_aliases
WHEN EXISTS (SELECT 1 FROM hosts WHERE name = NEW.alias)
BEGIN
    SELECT RAISE(ABORT, 'SSH alias already used as canonical host name');
END;

CREATE TRIGGER group_cycle_insert BEFORE INSERT ON group_children
BEGIN
    SELECT RAISE(ABORT, 'group relationship would create a cycle')
    WHERE NEW.parent_id IN (
        WITH RECURSIVE descendants(id) AS (
            SELECT NEW.child_id
            UNION
            SELECT g.child_id FROM group_children g JOIN descendants d ON g.parent_id = d.id
        )
        SELECT id FROM descendants
    );
END;
CREATE TRIGGER group_cycle_update BEFORE UPDATE ON group_children
BEGIN
    SELECT RAISE(ABORT, 'group relationship would create a cycle')
    WHERE NEW.parent_id IN (
        WITH RECURSIVE descendants(id) AS (
            SELECT NEW.child_id
            UNION
            SELECT g.child_id FROM group_children g JOIN descendants d ON g.parent_id = d.id
            WHERE g.parent_id != OLD.parent_id OR g.child_id != OLD.child_id
        )
        SELECT id FROM descendants
    );
END;
