package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"hostdb/internal/sshconfig"
	"hostdb/internal/store"
)

func (a *app) sshCommands() *cobra.Command {
	root := &cobra.Command{Use: "ssh", Short: "Manage and generate OpenSSH settings"}
	generate := command("generate", "Write the generated OpenSSH config to stdout", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		var data []byte
		err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
			return s.Transaction(ctx, func(q store.Queries) error {
				var err error
				data, err = sshData(ctx, q)
				return err
			})
		})
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	})
	var output string
	var once bool
	sync := command("sync", "Atomically write OpenSSH config and enable automatic sync", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		return a.withDB(cmd.Context(), store.Write, func(s *store.Store) error {
			return a.sync(cmd.Context(), s, output, false, !once)
		})
	})
	sync.Flags().StringVar(&output, "output", "", "File to write (defaults to saved path or ~/.ssh/config.d/hostdb.generated)")
	sync.Flags().BoolVar(&once, "once", false, "Write once without changing automatic sync settings")
	disable := command("disable-auto-sync", "Stop updating the generated config after database changes", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		return a.mutate(cmd.Context(), false, func(q store.Queries) error {
			return q.SetSyncPath(cmd.Context(), "")
		})
	})
	root.AddCommand(generate, sync, disable, a.optionCommands())
	return root
}

func sshData(ctx context.Context, q store.Queries) ([]byte, error) {
	hosts, err := q.SSHHosts(ctx, true)
	if err != nil {
		return nil, err
	}
	return sshconfig.Generate(hosts)
}

func (a *app) sync(ctx context.Context, s *store.Store, target string, automatic, configure bool) error {
	// Hold the SQLite writer lock from snapshot through publication. A slower
	// sync cannot replace a newer snapshot published by another process.
	return s.Transaction(ctx, func(q store.Queries) error {
		path := target
		if path == "" {
			var err error
			path, err = q.SyncPath(ctx)
			if err != nil {
				return err
			}
			if path == "" {
				if automatic {
					return nil
				}
				path, err = sshconfig.DefaultPath()
				if err != nil {
					return err
				}
			}
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		hosts, err := q.SSHHosts(ctx, true)
		if err != nil {
			return err
		}
		if err := sshconfig.Sync(ctx, path, hosts); err != nil {
			return fmt.Errorf("sync SSH file %q: %w", path, err)
		}
		if configure {
			return q.SetSyncPath(ctx, path)
		}
		return nil
	})
}

func (a *app) optionCommands() *cobra.Command {
	root := &cobra.Command{Use: "option", Short: "Manage ordered host-specific OpenSSH directives"}
	for _, verb := range []string{"set", "add", "remove"} {
		usage, nargs := verb+" HOST OPTION VALUE", cobra.ExactArgs(3)
		if verb == "remove" {
			usage, nargs = "remove HOST OPTION [VALUE]", cobra.RangeArgs(2, 3)
		}
		cmd := command(usage, verb+" a directive", nargs, func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			return a.mutate(ctx, true, func(q store.Queries) error {
				h, err := q.Host(ctx, args[0])
				if err != nil {
					return err
				}
				var value *string
				if len(args) == 3 {
					value = &args[2]
				}
				return q.Option(ctx, h.ID, verb, args[1], value)
			})
		})
		a.positional(cmd, "all-hosts")
		root.AddCommand(cmd)
	}
	var format string
	list := command("list HOST", "List SSH directives in configuration order", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		var opts []store.Option
		err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
			return s.Transaction(ctx, func(q store.Queries) error {
				host, err := q.Host(ctx, args[0])
				if err != nil {
					return err
				}
				opts, err = q.Options(ctx, host.ID)
				return err
			})
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		switch format {
		case "json":
			return prettyJSON(out, opts)
		case "text":
			for _, opt := range opts {
				if _, err := fmt.Fprintf(out, "%s %s\n", opt.Name, opt.Value); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unknown output format %q", format)
		}
	})
	list.Flags().StringVar(&format, "format", "text", "Output format (text, json)")
	a.positional(list, "all-hosts")
	root.AddCommand(list)
	return root
}
