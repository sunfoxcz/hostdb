package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"hostdb/internal/sshconfig"
	"hostdb/internal/store"
)

func (a *app) doctor(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	var issues []error
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		issues = append(issues, errors.New("OpenSSH client ssh is not on PATH"))
	} else {
		fmt.Fprintln(out, "OpenSSH client:", ssh)
	}
	for _, tool := range []string{"ansible", "ansible-inventory"} {
		path, err := exec.LookPath(tool)
		if err == nil {
			fmt.Fprintln(out, tool+":", path)
		} else {
			fmt.Fprintln(out, tool+" is not installed or not on PATH (optional).")
		}
	}
	database, err := store.Path(a.dbPath)
	if err != nil {
		return errors.Join(append(issues, err)...)
	}
	var hosts []store.Host
	var generated string
	err = a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
		return s.Transaction(ctx, func(q store.Queries) error {
			var err error
			generated, err = q.SyncPath(ctx)
			if err != nil {
				return err
			}
			hosts, err = q.SSHHosts(ctx, true)
			return err
		})
	})
	if err != nil {
		return errors.Join(append(issues, err)...)
	}
	fmt.Fprintln(out, "SQLite database:", database)
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.Join(append(issues, err)...)
	}
	if generated == "" {
		generated, err = sshconfig.DefaultPath()
		if err != nil {
			return errors.Join(append(issues, err)...)
		}
	}
	manual := filepath.Join(home, ".ssh", "config")
	for _, path := range []string{filepath.Dir(database), filepath.Dir(manual), manual, filepath.Dir(generated)} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().Perm()&0022 != 0 {
			issues = append(issues, fmt.Errorf("%q is writable by other users", path))
		} else if err != nil && !os.IsNotExist(err) {
			issues = append(issues, err)
		}
	}
	if err := sshconfig.Check(ctx, out, ssh, manual, generated, hosts); err != nil {
		issues = append(issues, err)
	}
	return errors.Join(issues...)
}
