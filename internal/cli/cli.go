package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"hostdb/internal/inventory"
	"hostdb/internal/store"
)

type app struct {
	dbPath string
}

type action func(*cobra.Command, []string) error

func command(use, short string, args cobra.PositionalArgs, run action) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: run}
}

func New() *cobra.Command {
	a := &app{}
	root := command("hostdb", "Manage hosts for OpenSSH and Ansible", cobra.NoArgs, nil)
	root.SilenceUsage = true
	root.SilenceErrors = true
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&a.dbPath, "db", "", "SQLite database (overrides HOSTDB_DB and XDG_DATA_HOME)")
	a.inventoryFlags(root, true)
	inv := command("inventory", "Emit Ansible executable inventory JSON", cobra.NoArgs, nil)
	a.inventoryFlags(inv, false)

	root.AddCommand(
		command("init", "Initialize or migrate the database", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
			return a.withDB(cmd.Context(), store.Initialize, func(*store.Store) error { return nil })
		}),
		a.hostCommands(), a.groupCommands(), a.sshCommands(), inv, a.importCommands(), a.completionCommands(root),
		command("validate", "Check consistency and data in SQLite", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
				return s.Transaction(ctx, func(q store.Queries) error { return q.Validate(ctx) })
			}); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Database is valid.")
			return err
		}),
		command("doctor", "Check SQLite, OpenSSH configuration and Ansible availability", cobra.NoArgs, a.doctor),
		command("backup PATH", "Create a consistent SQLite backup", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
			return a.withDB(cmd.Context(), store.ReadOnly, func(s *store.Store) error {
				return s.Backup(cmd.Context(), args[0])
			})
		}),
	)
	return root
}

func (a *app) withDB(ctx context.Context, mode store.Mode, fn func(*store.Store) error) error {
	path, err := store.Path(a.dbPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, path, mode)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(s)
}

func (a *app) mutate(ctx context.Context, sshChanged bool, apply func(store.Queries) error) error {
	return a.withDB(ctx, store.Write, func(s *store.Store) error {
		var syncPath string
		if err := s.Transaction(ctx, func(q store.Queries) error {
			if err := apply(q); err != nil {
				return err
			}
			if !sshChanged {
				return nil
			}
			var err error
			syncPath, err = q.SyncPath(ctx)
			return err
		}); err != nil {
			return err
		}
		if syncPath != "" {
			if err := a.sync(ctx, s, "", true, false); err != nil {
				return fmt.Errorf("database changes committed; generated SSH file may be stale: %w", err)
			}
		}
		return nil
	})
}

func (a *app) inventoryFlags(cmd *cobra.Command, root bool) {
	var list bool
	var host string
	cmd.Flags().BoolVar(&list, "list", false, "Return all inventory as JSON")
	cmd.Flags().StringVar(&host, "host", "", "Return variables for a canonical host")
	cmd.MarkFlagsMutuallyExclusive("list", "host")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !list && !cmd.Flags().Changed("host") {
			if root && !cmd.Flags().Changed("list") {
				return cmd.Help()
			}
			return errors.New("specify --list or --host NAME")
		}
		if !list {
			if err := store.HostName(host); err != nil {
				return err
			}
		}
		ctx := cmd.Context()
		var result any = inventory.Vars{}
		err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
			if !list {
				// No host variables are stored in SQLite. Unknown and disabled hosts
				// likewise have no variables to return.
				return nil
			}
			return s.Transaction(ctx, func(q store.Queries) error {
				var err error
				result, err = inventory.List(ctx, q)
				return err
			})
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	_ = cmd.RegisterFlagCompletionFunc("host", func(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return a.complete(cmd, "hosts", prefix)
	})
}

func prettyJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func printNames(out io.Writer, names []string) error {
	if len(names) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(out, strings.Join(names, "\n"))
	return err
}
