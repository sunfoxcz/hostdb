package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"hostdb/internal/sshconfig"
	"hostdb/internal/store"
)

func (a *app) checkCommand() *cobra.Command {
	var databaseOnly bool
	cmd := command("check", "Check database integrity, installed tools, and SSH configuration", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		database := checkTable{title: "Database"}
		config, err := a.checkDatabase(cmd.Context(), &database, databaseOnly)
		tables := []checkTable{database}
		if !databaseOnly {
			tools := checkTable{title: "Tools"}
			ssh := checkTools(&tools)
			configTable := checkTable{title: "SSH configuration"}
			if err != nil {
				configTable.add("Configuration", "SKIP", "Database settings are unavailable")
			} else {
				checkSSH(cmd.Context(), &configTable, ssh, config)
			}
			tables = append(tables, tools, configTable)
		}

		var report strings.Builder
		failed := false
		for _, table := range tables {
			table.write(&report)
			failed = failed || table.failed
		}
		if _, err := io.WriteString(cmd.OutOrStdout(), report.String()); err != nil {
			return err
		}
		if failed {
			return errors.New("check failed")
		}
		return nil
	})
	cmd.Flags().BoolVar(&databaseOnly, "database-only", false, "Check only the database")
	return cmd
}

type sshCheckInputs struct {
	generated string
	hosts     []store.Host
}

func (a *app) checkDatabase(ctx context.Context, table *checkTable, databaseOnly bool) (sshCheckInputs, error) {
	var config sshCheckInputs
	path, err := store.Path(a.dbPath)
	if err != nil {
		table.record("SQLite database", "", err)
		return config, err
	}
	db, err := store.Open(ctx, path, store.ReadOnly)
	table.record("SQLite database", path, err)
	if err != nil {
		table.add("Integrity and data", "SKIP", "Database could not be opened")
		return config, err
	}
	defer db.Close()
	table.permissions("Database directory", filepath.Dir(path))

	var validationErr, configErr error
	err = db.Transaction(ctx, func(q store.Queries) error {
		validationErr = q.Validate(ctx)
		if !databaseOnly {
			config.generated, configErr = q.SyncPath(ctx)
			if configErr == nil {
				config.hosts, configErr = q.SSHHosts(ctx, true)
			}
		}
		return nil
	})
	table.record("Integrity and data", "SQLite integrity, foreign keys, records, names, and group relationships", errors.Join(validationErr, err))
	if configErr != nil {
		table.record("SSH data", "", configErr)
	}
	return config, errors.Join(configErr, err)
}

func checkTools(table *checkTable) string {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		ssh = ""
	}
	table.record("OpenSSH client", ssh, err)
	for _, tool := range []string{"ansible", "ansible-inventory"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			table.add(tool, "SKIP", "Optional tool is not on PATH")
		} else {
			table.record(tool, path, nil)
		}
	}
	return ssh
}

func checkSSH(ctx context.Context, table *checkTable, ssh string, config sshCheckInputs) {
	home, err := os.UserHomeDir()
	if err != nil {
		table.record("User configuration", "", err)
		return
	}
	manual := filepath.Join(home, ".ssh", "config")
	if config.generated == "" {
		config.generated, err = sshconfig.DefaultPath()
		if err != nil {
			table.record("Generated config", "", err)
			return
		}
	}
	table.permissions("SSH directory", filepath.Dir(manual))
	table.permissions("User config", manual)
	if filepath.Dir(config.generated) != filepath.Dir(manual) {
		table.permissions("Generated config directory", filepath.Dir(config.generated))
	}
	sshconfig.Check(ctx, ssh, manual, config.generated, config.hosts, table.record)
	if ssh == "" {
		table.add("Effective settings", "SKIP", "OpenSSH is unavailable")
	}
}

type checkTable struct {
	title  string
	rows   []checkRow
	failed bool
}

type checkRow struct {
	name, status, detail string
}

func (t *checkTable) add(name, status, detail string) {
	t.rows = append(t.rows, checkRow{name, status, detail})
}

func (t *checkTable) record(name, detail string, err error) {
	if err != nil {
		t.failed = true
		t.add(name, "FAIL", err.Error())
	} else {
		t.add(name, "OK", detail)
	}
}

func (t *checkTable) permissions(name, path string) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		t.add(name, "SKIP", fmt.Sprintf("%q does not exist", path))
	case err != nil:
		t.record(name, "", err)
	case info.Mode().Perm()&0022 != 0:
		t.record(name, "", fmt.Errorf("%q is writable by other users", path))
	default:
		t.record(name, path, nil)
	}
}

func (t *checkTable) write(out *strings.Builder) {
	if out.Len() > 0 {
		out.WriteByte('\n')
	}
	fmt.Fprintln(out, t.title)
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	const checkHeading, statusHeading, detailsHeading = "CHECK", "STATUS", "DETAILS"
	checkWidth := len(checkHeading)
	for _, row := range t.rows {
		checkWidth = max(checkWidth, utf8.RuneCountInString(row.name))
	}
	fmt.Fprintf(table, "%s\t%s\t%s\n", checkHeading, statusHeading, detailsHeading)
	fmt.Fprintf(table, "%s\t%s\t%s\n",
		strings.Repeat("─", checkWidth),
		strings.Repeat("─", len(statusHeading)),
		strings.Repeat("─", len(detailsHeading)),
	)
	for _, row := range t.rows {
		detail, rest, _ := strings.Cut(strings.ReplaceAll(row.detail, "\t", "    "), "\n")
		fmt.Fprintf(table, "%s\t%s\t%s\n", row.name, row.status, detail)
		for _, line := range strings.Split(rest, "\n") {
			if line != "" {
				fmt.Fprintf(table, "\t\t%s\n", line)
			}
		}
	}
	table.Flush()
}
