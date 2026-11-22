# Working on hostdb

`hostdb` is a Go CLI for managing hosts and groups in local SQLite. It builds as a single executable without CGO. The minimum Go version is declared in `go.mod`.

## Project layout

- `cmd/hostdb` is the executable entry point, and `internal/cli` defines the commands.
- `internal/store` owns SQLite queries and embedded migrations. `internal/inventory`, `internal/sshconfig`, and `internal/importer` handle their corresponding formats.
- `tests` contains CLI and Ansible integration tests and benchmarks. `examples` contains runnable configuration samples.

## Conventions

- Write documentation, comments, identifiers, and CLI messages in English. Use fictional names, reserved example domains, and documentation IP ranges in examples and tests. Use loopback for local connection tests. Keep real client identifiers, credentials, and private keys out of project files.
- Keep canonical host names, SSH aliases, and network addresses distinct. Ansible uses canonical names only. OpenSSH resolves both canonical names and aliases from the generated config. Ansible owns `group_vars`, `host_vars`, and variable precedence.
- Keep inventory reads read-only and output deterministic. Emit only valid JSON on inventory stdout and send diagnostics to stderr. Keep the completion list commands suitable for scripts.
- Use parameterized SQL and transactions for mutations. Add a numbered migration for schema changes so existing databases can upgrade. Preserve foreign keys and cycle checks for the group graph.
- Keep generated SSH file updates atomic and scoped to the generated file. Report a committed database change if automatic SSH sync fails. Preserve directive order and repeated values.
- Use temporary databases and config files for verification. Run integration tests against local fixtures. Update the README and examples when user-facing behavior changes.

## Verification

For Go changes, format touched files with `gofmt` and run the relevant checks from the repository root.

```sh
make vet
make test
make build
```

Run `go test -v ./tests -run TestAnsibleIntegration` for targeted Ansible checks. The tests skip when the required tools or local SSH environment are unavailable. Use `make bench` for changes that affect lookups, inventory generation, or CLI startup.
