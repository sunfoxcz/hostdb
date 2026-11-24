CREATE TRIGGER host_group_name_insert BEFORE INSERT ON hosts
WHEN EXISTS (SELECT 1 FROM groups WHERE name = NEW.name)
BEGIN
    SELECT RAISE(ABORT, 'host name already used as group name');
END;
CREATE TRIGGER host_group_name_update BEFORE UPDATE OF name ON hosts
WHEN NEW.name <> OLD.name AND EXISTS (SELECT 1 FROM groups WHERE name = NEW.name)
BEGIN
    SELECT RAISE(ABORT, 'host name already used as group name');
END;

CREATE TRIGGER ssh_alias_group_name_insert BEFORE INSERT ON ssh_aliases
WHEN EXISTS (SELECT 1 FROM groups WHERE name = NEW.alias)
BEGIN
    SELECT RAISE(ABORT, 'SSH alias already used as group name');
END;
CREATE TRIGGER ssh_alias_group_name_update BEFORE UPDATE OF alias ON ssh_aliases
WHEN NEW.alias <> OLD.alias AND EXISTS (SELECT 1 FROM groups WHERE name = NEW.alias)
BEGIN
    SELECT RAISE(ABORT, 'SSH alias already used as group name');
END;

CREATE TRIGGER group_ssh_name_insert BEFORE INSERT ON groups
BEGIN
    SELECT RAISE(ABORT, 'group name already used as canonical host name')
    WHERE EXISTS (SELECT 1 FROM hosts WHERE name = NEW.name);
    SELECT RAISE(ABORT, 'group name already used as SSH alias')
    WHERE EXISTS (SELECT 1 FROM ssh_aliases WHERE alias = NEW.name);
END;
CREATE TRIGGER group_ssh_name_update BEFORE UPDATE OF name ON groups
WHEN NEW.name <> OLD.name
BEGIN
    SELECT RAISE(ABORT, 'group name already used as canonical host name')
    WHERE EXISTS (SELECT 1 FROM hosts WHERE name = NEW.name);
    SELECT RAISE(ABORT, 'group name already used as SSH alias')
    WHERE EXISTS (SELECT 1 FROM ssh_aliases WHERE alias = NEW.name);
END;
