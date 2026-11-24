# hostdb

`hostdb` tracks servers and network devices in a local SQLite database. It generates OpenSSH client configuration and serves current Ansible dynamic inventory on each request. It runs as a single executable.

| Field | Example | Purpose |
|---|---|---|
| Canonical name | `app-server` | Ansible, `inventory_hostname`, OpenSSH |
| SSH alias | `app-ssh` | Additional name for the same OpenSSH block |
| Network address | `app-server.example.com` | Actual connection target |

The examples use fictional hostnames and documentation IP addresses.

## Installation on Ubuntu

### Install a release

Download the archive for Linux x86-64 (`amd64`) or ARM64 (`arm64`) and `SHA256SUMS`. If you have GitHub CLI, you can download both files from the terminal. [GitHub Releases](https://github.com/sunfoxcz/hostdb/releases)

```sh
version=v1.0.0
arch=amd64
archive="hostdb-$version-linux-$arch.tar.gz"
gh release download "$version" --repo sunfoxcz/hostdb --pattern "$archive" --pattern SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf "$archive" hostdb
install -Dm755 hostdb "$HOME/.local/bin/hostdb"
export PATH="$HOME/.local/bin:$PATH"
```

Use `gh attestation verify "$archive" --repo sunfoxcz/hostdb` to verify the archive's build provenance on GitHub. [GitHub artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations)

### Build from source

Build with Go 1.25 or later. If your Ubuntu repositories offer an older version, install Go from the official distribution. Install the OpenSSH client and Ansible separately according to the integrations you use. [Go installation guide](https://go.dev/doc/install)

```sh
sudo apt update
sudo apt install make openssh-client
# Optional Ansible integration
sudo apt install ansible-core

go version
make build
make install
export PATH="$HOME/.local/bin:$PATH"
```

Add the `PATH` export to `~/.zshrc` to keep it across sessions. The default installation path is `~/.local/bin/hostdb`. You can also build the binary directly with Go and install it yourself.

```sh
CGO_ENABLED=0 go build -trimpath -o hostdb ./cmd/hostdb
install -Dm755 hostdb "$HOME/.local/bin/hostdb"
```

### Initialize a database

Run `hostdb init` once when setting up a new database.

The binary embeds SQLite via `modernc.org/sqlite`. The `sqlite3` command is optional. The default database path is `~/.local/share/hostdb/hosts.db`, or `$XDG_DATA_HOME/hostdb/hosts.db` when `XDG_DATA_HOME` is an absolute path. `HOSTDB_DB` overrides the default, and `--db` overrides both.

```sh
export HOSTDB_DB="$HOME/.local/share/hostdb/infra.db"
hostdb init
hostdb --db /srv/private/hosts.db init
```

For an initialized database, `hostdb init` reports its state and suggests `hostdb upgrade` when the schema is older.

After replacing the binary, the first read or write that needs a schema migration applies it automatically. Inventory queries use a read-only connection once the schema is current. Backups preserve the source schema, and import dry runs apply any migrations to the temporary copy.

To migrate ahead of time, for example before running Ansible with read-only access, run `hostdb upgrade` with write access to the database. If the selected database file is missing, the command reports an error and suggests `hostdb init`. It succeeds without changes for an empty file or an up-to-date database.

```sh
hostdb --db /srv/private/hosts.db upgrade
```

SQLite may use `-wal` and `-shm` files in the same directory when WAL mode is active.

## Hosts, aliases, and SSH settings

```sh
hostdb host add app-server \
    --hostname app-server.example.com --user root --ssh-alias app-ssh
hostdb host add switch-01 --hostname 192.0.2.85 --user root
hostdb ssh option set switch-01 PasswordAuthentication yes
hostdb ssh option set switch-01 KexAlgorithms '+diffie-hellman-group1-sha1'
hostdb ssh option set switch-01 HostKeyAlgorithms '+ssh-rsa'
hostdb ssh option set switch-01 MACs hmac-sha1
hostdb ssh option set switch-01 Ciphers aes128-ctr

hostdb host add router-01 --hostname 198.51.100.20 --port 2222 --user netops
hostdb ssh option set router-01 PubkeyAcceptedAlgorithms '+ssh-rsa'

hostdb host list
hostdb host list --format json
hostdb host list --format names
hostdb host show app-server
hostdb host show app-server --format json

hostdb host alias add app-server app-alt
hostdb host alias remove app-server app-alt
hostdb host set app-server description 'Production web server'
hostdb host set app-server hostname app-server.example.com
```

`host set` updates `hostname`, `user`, `port`, `description`, or `enabled`. The hostname defaults to the canonical name and the port defaults to 22. With an empty username, OpenSSH chooses the user as usual. `host disable` hides the host from inventory and generated SSH configuration while retaining its record and group memberships. `host enable` makes it available again. `host remove` deletes the record, its aliases, directives, and memberships.

`host rename OLD NEW` retains memberships and directives. If `NEW` is already an alias of the host, the command promotes it to the canonical name. You can then add the old name as an alias. Update any corresponding `host_vars` entries and playbook references.

SSH names use lowercase letters, digits, underscores, dots, and hyphens. A network address can be a DNS name, an IPv4 address, or an IPv6 address. Each SSH name belongs to one host.

```sh
hostdb ssh option add app-server IdentityFile '~/.ssh/id_ed25519'
hostdb ssh option add app-server IdentityFile '"~/.ssh/team keys/id_ed25519"'
hostdb ssh option set app-server IdentitiesOnly yes
hostdb ssh option set app-server ServerAliveInterval 30
hostdb ssh option list app-server
hostdb ssh option list app-server --format json
hostdb ssh option remove app-server IdentityFile '"~/.ssh/team keys/id_ed25519"'
```

`add` appends a directive to the block. `set` replaces all values for that directive with one value at its first occurrence. `remove HOST OPTION` removes all values for the directive. Pass `VALUE` to remove only matching values. Directive names are case-insensitive. Values use OpenSSH syntax. For paths containing spaces, keep the double quotes inside the value as shown above.

Host-specific options include `ProxyJump`, `ProxyCommand`, `ForwardAgent`, `ServerAliveCountMax`, `ConnectTimeout`, `StrictHostKeyChecking`, and `UserKnownHostsFile`. Use `host set` for `HostName`, `User`, and `Port`. Stored options exclude structural directives such as `Host`, `Match`, `Include`, and `IgnoreUnknown`. Store private key paths in hostdb and keep credentials in a secrets manager or Ansible Vault. Use trusted values for directives such as `ProxyCommand`, which OpenSSH can execute when connecting.

Algorithm list additions with `+` apply only to the host blocks that specify them. `hostdb doctor` reports directives or algorithm lists that the installed OpenSSH client rejects and preserves the stored values. Use `ssh -Q kex` and `ssh -Q key` to list available algorithms.

## Groups and child groups

```sh
for group in php php56 php71 php72 ubuntu ubuntu20 ubuntu22 ubuntu24 ubuntu26; do
    hostdb group add "$group"
done
for child in php56 php71 php72; do
    hostdb group child add php "$child"
done
for child in ubuntu20 ubuntu22 ubuntu24 ubuntu26; do
    hostdb group child add ubuntu "$child"
done

hostdb host add web01 --hostname web01.example.com --user deploy
hostdb group host add php56 web01
hostdb group host add ubuntu24 web01
hostdb group host add ubuntu24 app-server

hostdb group list
hostdb group list --format names
hostdb group show ubuntu24
hostdb group tree php
hostdb group tree ubuntu
hostdb host list --group php --format names
hostdb host list --group ungrouped
```

A group can have multiple parents, and a host can be a direct member of multiple groups. Both `php56` and `php` target `web01`. `group show` lists direct relationships, and `group tree` shows the hierarchy. The tree expands shared branches on their first occurrence. Use `group host remove GROUP HOST` and `group child remove PARENT CHILD` to remove relationships. SQLite rejects cycles and duplicate relationships.

`host list --group NAME` includes direct members and members of descendant groups. Each host is listed once, including disabled hosts. `--group all` lists every host, and `--group ungrouped` lists hosts with no group memberships. The filter works with table, JSON, and name output.

Group names can contain letters, digits, underscores, dots, and hyphens. Names that match `[A-Za-z_][A-Za-z0-9_]*` also work with Ansible configurations that rewrite invalid identifiers. Inventory creates `all` and `ungrouped` automatically and reserves `_meta` for metadata. A stored group name must differ from every canonical host name and SSH alias, including those of disabled hosts. This rule also applies to imports.

## OpenSSH

```sh
hostdb ssh generate
hostdb ssh sync
```

Add the following line near the top of `~/.ssh/config`, before the first `Host` or `Match` block. An `Include` inside a block applies only when that block matches, regardless of indentation. OpenSSH uses the first value it finds for most directives, so settings in an earlier `Host *` block can take precedence over the generated settings. After checking the effective settings, you can consolidate any duplicate blocks. [OpenSSH ssh_config(5)](https://man.openbsd.org/ssh_config)

```sshconfig
Include ~/.ssh/config.d/hostdb.generated
```

```sh
ssh -G app-ssh
ssh -G app-server
hostdb doctor
```

Both `ssh -G` commands should report `hostname app-server.example.com`, `user root`, and `port 22`. If OpenSSH is available locally, `hostdb` checks the generated configuration before replacing the previous file. If the check fails, the previous file stays in place. Successful writes are atomic and use permissions `0600`.

`ssh sync` stores the output path in the database and enables automatic updates after changes to hosts, aliases, or SSH options. Use `ssh sync --output /path/to/hostdb.generated` to set a different path. Give each database with automatic sync its own output file. `ssh sync --once` writes the file once, and `ssh disable-auto-sync` turns off automatic updates.

If a database change commits and automatic sync fails, the command exits with a nonzero status and reports the committed change. Fix the reported issue and rerun `hostdb ssh sync`. Synchronization refuses to overwrite symbolic links and files that lack the generated configuration header.

## Ansible dynamic inventory

The binary implements the Ansible script inventory plugin interface. `--list` returns the current inventory structure and `_meta.hostvars`. `--host` returns an empty variables object because hostdb stores inventory structure only. [Ansible script inventory plugin](https://docs.ansible.com/ansible/latest/collections/ansible/builtin/script_inventory.html)

```sh
hostdb inventory --list
hostdb inventory --host app-server
ansible -i "$(command -v hostdb)" app-server -m ansible.builtin.ping
```

For an existing project, place the executable wrapper alongside `group_vars` and `host_vars`. You can copy the included example. [inventory-hostdb](examples/ansible/inventory-hostdb)

```sh
#!/bin/sh
exec hostdb inventory "$@"
```

Set `inventory = ./inventory-hostdb` under `[defaults]` in the project's `ansible.cfg`. Make the wrapper executable with `chmod +x inventory-hostdb`. To use a different database, export `HOSTDB_DB` before running Ansible or use `exec hostdb --db /absolute/path/hosts.db inventory "$@"` in the wrapper.

```text
ansible/
├── ansible.cfg
├── inventory-hostdb
├── group_vars/
│   ├── all.yml
│   ├── php56.yml
│   └── ubuntu24.yml
├── host_vars/
│   └── app-server.yml
└── playbooks/
    └── uptime.yml
```

Run these commands from the project directory.

```sh
ansible-inventory --list
ansible-inventory --graph
ansible-inventory --host app-server
ansible app-server -m ansible.builtin.ping
ansible-playbook playbooks/uptime.yml
```

`hostdb inventory --list` shows groups, direct memberships, child relationships, and canonical host names from SQLite. `ansible-inventory --list` shows the inventory after Ansible processes it, including variables loaded from `group_vars` and `host_vars`. Keep your existing files in place. Ansible handles YAML, Vault, and variable precedence. If you use the binary directly as an inventory source outside the project, specify the project with `--playbook-dir`. From the project directory, run `ansible-inventory -i "$(command -v hostdb)" --playbook-dir "$PWD" --list`. [Ansible host_group_vars](https://docs.ansible.com/ansible/latest/collections/ansible/builtin/host_group_vars_vars.html) · [Ansible inventory and inheritance](https://docs.ansible.com/ansible/latest/inventory_guide/intro_inventory.html)

The default `ansible.builtin.ssh` connection plugin passes the canonical name to OpenSSH. OpenSSH gets the address, port, username, and other settings from `Host app-server app-ssh`. Hostdb leaves `ansible_host`, `ansible_port`, and `ansible_user` unset so OpenSSH can supply those values. Setting them in Ansible variables or arguments can override OpenSSH settings. Plugins such as `paramiko_ssh` and some network connection plugins require their own target, authentication, and transport settings. [Ansible SSH connection plugin](https://docs.ansible.com/ansible/latest/collections/ansible/builtin/ssh_connection.html)

## Import

Review the plan and warnings first. Both imports are transactional and safe to repeat. `--dry-run` runs the same checks on a temporary copy and also works before you initialize the database.

```sh
hostdb import ansible-ini ./ansible/hosts --dry-run
hostdb import ansible-ini ./ansible/hosts
hostdb import ssh-config ~/.ssh/config --map app-ssh=app-server --dry-run
hostdb import ssh-config ~/.ssh/config --map app-ssh=app-server
```

The Ansible INI importer supports hosts, groups, `:children` sections, inline `host:port` syntax, and the connection variables `ansible_host`, `ansible_user`, and `ansible_port`, including their `ansible_ssh_*` equivalents. It reports unsupported host and group variable names on stderr. Move those variables to `host_vars` or `group_vars` as needed. Expand host ranges into individual names. If you rename an invalid group, update its vars files too.

The SSH importer reads explicit `Host` blocks and merges names in a block that share a `HostName`. Without an existing match or mapping, it uses the first name as the canonical name. It reports and skips `Match` blocks, wildcards, `Include` directives, and global settings. You can import included files directly. An explicit source value can replace a default address equal to the canonical name, an empty username, or port 22. Reconcile other value conflicts and directive-order conflicts manually.

The importer matches names using an existing canonical name, a stored alias, or an explicit `--map SSH=CANONICAL`. A shared address alone triggers a possible conflict. Use `--allow-shared-address` for distinct hosts that intentionally share an address. If you imported a server under its SSH name, you can rename the record to the canonical name and add the old name as an alias. Complete sample inputs are available in `examples/ansible/legacy.ini` and `examples/ssh/legacy.config`.

## Zsh and automation

```sh
mkdir -p "$HOME/.zsh/completions"
hostdb completion zsh > "$HOME/.zsh/completions/_hostdb"
```

In `~/.zshrc`, add the directory to `fpath` before calling `compinit`. If you have yet to configure `compinit`, use the following setup.

```zsh
fpath=("$HOME/.zsh/completions" $fpath)
autoload -Uz compinit
compinit
```

Completion suggests subcommands, flags, hosts, and groups based on argument position. Read the name lists directly for a custom wrapper or Zsh cache.

```sh
hostdb completion hosts
hostdb completion groups
hostdb completion targets
hostdb completion ssh-aliases
```

The lists are sorted and contain one name per line. `hosts` and `ssh-aliases` list enabled hosts and their aliases, respectively. `targets` lists enabled hosts and groups, including `all` and `ungrouped`. Use `host list --format names` to include disabled hosts. Errors and warnings go to stderr. Failed commands return a nonzero exit status.

## Backup and restore

```sh
mkdir -p -m 700 "$HOME/backups/hostdb"
hostdb backup "$HOME/backups/hostdb/hosts-$(date +%F).db"
```

The backup command uses `VACUUM INTO` to take a consistent SQLite snapshot, including committed changes in the WAL. It refuses to overwrite an existing file. Restore to a new database path and verify its contents first. [SQLite VACUUM INTO](https://sqlite.org/lang_vacuum.html#vacuuminto)

```sh
install -Dm600 "$HOME/backups/hostdb/hosts-2026-11-22.db" \
    "$HOME/.local/share/hostdb/restored.db"
export HOSTDB_DB="$HOME/.local/share/hostdb/restored.db"
hostdb validate
hostdb inventory --list
hostdb ssh sync
```

The backup includes the saved automatic sync path. If you run the restored database alongside the original, use `hostdb ssh sync --output ...` to give it a separate file and add a corresponding `Include`. Back up hand-written SSH configuration, SSH keys, and Ansible project files separately.

## Troubleshooting

| Symptom | What to check |
|---|---|
| The database is missing | Check `HOSTDB_DB` or `--db` and run `hostdb init` to create it. The database should have permissions `0600` and reside in a private directory. |
| An automatic schema upgrade fails because the database is read-only | Run `hostdb upgrade` with write access to the selected database, then retry the command. |
| `ssh -G` reports a different user, address, or port | Check the `Include` placement and block order described in [OpenSSH](#openssh), then run `hostdb doctor`. |
| OpenSSH rejects an algorithm or directive | `hostdb doctor` reports the host name and the OpenSSH error. Availability depends on the installed client and its cryptographic libraries. |
| Ansible falls back to implicit localhost | Verify that the wrapper is executable, `hostdb` is on `PATH`, and `ansible-inventory -i ./inventory-hostdb --graph` uses the intended database. |
| Ansible warns about a host and group with the same name | Run `hostdb validate` to find existing collisions. Use `hostdb host rename OLD NEW` to resolve a canonical name collision and update the matching `host_vars` and playbook references. Use `hostdb host alias remove HOST ALIAS` to resolve an alias collision. |
| Inventory variables are missing | Check the variable files alongside the inventory source, verify that names match exactly, and inspect `ansible-inventory --host NAME`. |
| Ansible targets the wrong host | Check `ansible-inventory --host NAME` and the connection plugin settings. `ansible NAME -m ping -vvvv` opens a connection and prints the details. |

`hostdb validate` checks SQLite integrity, foreign keys, record formats, name collisions, and group cycles. `hostdb doctor` checks tool availability, file permissions, whether the generated file is current, the `Include` directive, and overrides in user configuration. It runs `ssh -G` on a safe copy of the user configuration without opening a connection. If it encounters a `Match` block or an unsupported `Include` path expansion, it reports the limitation and checks the generated blocks individually. The check covers user-level OpenSSH configuration only.

## Tests and performance

```sh
make vet
make test
go test -v ./tests -run TestAnsibleIntegration
make bench
```

The integration test runs Ansible to verify the group graph, inheritance, and loading of `group_vars` and `host_vars`. The `ansible.builtin.ping` subtest requires `sshd`, `ssh-keygen`, and an available loopback interface. It starts a temporary `sshd` with test keys. On Ubuntu, the `openssh-server` package provides the server for this test. Tests skip with an explanation when required tools are missing.

`BenchmarkSQL` measures indexed host and group lookups on an open database. `BenchmarkInventory` measures snapshot reads and JSON encoding for 100, 1,000, and 10,000 hosts. `BenchmarkCLI` measures the entire process, including binary startup, database open, queries, and output. Inventory reads names and relationships in a few bulk queries.

## Creating a release

Create an annotated `vMAJOR.MINOR.PATCH` tag on a tested commit on `master`. Write the release notes in the tag message and push the tag to GitHub.

```sh
git switch master
git pull --ff-only
git tag -a v1.0.1 -m 'Describe the changes in this release.'
git push origin v1.0.1
```

GitHub Actions tests and builds static Linux binaries on native amd64 and arm64 runners. After both jobs pass, it publishes the archives, their SHA-256 checksums, and build attestations. The release body comes from the annotated tag.
