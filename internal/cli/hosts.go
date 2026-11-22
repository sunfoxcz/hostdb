package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"hostdb/internal/store"
)

func (a *app) hostCommands() *cobra.Command {
	root := &cobra.Command{Use: "host", Short: "Manage canonical hosts and SSH aliases"}
	var hostname, user, description string
	var port int
	var disabled bool
	var aliases []string
	add := command("add NAME", "Register a host", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		address := hostname
		if !cmd.Flags().Changed("hostname") {
			address = args[0]
		}
		h := store.Host{
			Name: args[0], Hostname: address, User: user, Port: port, Description: description, Enabled: !disabled,
		}
		return a.mutate(ctx, true, func(q store.Queries) error {
			id, err := q.CreateHost(ctx, h)
			if err != nil {
				return err
			}
			for _, name := range aliases {
				if err := q.Alias(ctx, id, name, true); err != nil {
					return err
				}
			}
			return nil
		})
	})
	add.Flags().StringVar(&hostname, "hostname", "", "Network address (defaults to NAME)")
	add.Flags().StringVar(&user, "user", "", "OpenSSH username")
	add.Flags().IntVar(&port, "port", 22, "OpenSSH port")
	add.Flags().StringVar(&description, "description", "", "Description")
	add.Flags().StringArrayVar(&aliases, "ssh-alias", nil, "Additional SSH name (repeatable)")
	add.Flags().BoolVar(&disabled, "disabled", false, "Register without enabling the host")

	var format, group string
	list := command("list", "List hosts, including disabled hosts", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		if format != "table" && format != "json" && format != "names" {
			return fmt.Errorf("unknown output format %q", format)
		}
		if cmd.Flags().Changed("group") && group == "" {
			return errors.New("--group requires a group name")
		}
		ctx := cmd.Context()
		var hosts []store.Host
		var names []string
		err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
			return s.Transaction(ctx, func(q store.Queries) error {
				var err error
				if format == "names" {
					names, err = q.HostNames(ctx, group)
				} else {
					hosts, err = q.Hosts(ctx, group)
				}
				return err
			})
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		switch format {
		case "names":
			return printNames(out, names)
		case "json":
			return prettyJSON(out, hosts)
		default:
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "NAME\tHOSTNAME\tUSER\tPORT\tENABLED\tDESCRIPTION")
			for _, h := range hosts {
				fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%t\t%s\n", h.Name, h.Hostname, h.User, h.Port, h.Enabled, h.Description)
			}
			return table.Flush()
		}
	})
	list.Flags().StringVar(&format, "format", "table", "Output format (table, json, names)")
	list.Flags().StringVar(&group, "group", "", "Filter by a group and its descendants")
	_ = list.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "json", "names"}, cobra.ShellCompDirectiveNoFileComp))
	_ = list.RegisterFlagCompletionFunc("group", func(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return a.complete(cmd, "inventory-groups", prefix)
	})

	var showFormat string
	show := command("show NAME", "Show host details", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		var host store.Host
		err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
			return s.Transaction(ctx, func(q store.Queries) error {
				var err error
				host, err = q.HostDetails(ctx, args[0])
				return err
			})
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		switch showFormat {
		case "json":
			return prettyJSON(out, host)
		case "text":
			_, err := fmt.Fprintf(out, "%s\n  hostname: %s\n  user: %s\n  port: %d\n  enabled: %t\n  description: %s\n  SSH aliases: %s\n  groups: %s\n",
				host.Name, host.Hostname, host.User, host.Port, host.Enabled, host.Description, strings.Join(host.Aliases, ", "), strings.Join(host.Groups, ", "))
			if err != nil {
				return err
			}
			for _, opt := range host.Options {
				if _, err := fmt.Fprintf(out, "  %s %s\n", opt.Name, opt.Value); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unknown output format %q", showFormat)
		}
	})
	show.Flags().StringVar(&showFormat, "format", "text", "Output format (text, json)")
	a.positional(show, "all-hosts")

	set := command("set NAME FIELD VALUE", "Set hostname, user, port, description or enabled", cobra.ExactArgs(3), func(cmd *cobra.Command, args []string) error {
		field := args[1]
		if field == "name" {
			return errors.New("use host rename to change the canonical name")
		}
		return a.mutate(cmd.Context(), field != "description", func(q store.Queries) error {
			return q.SetHost(cmd.Context(), args[0], field, args[2])
		})
	})
	set.ValidArgsFunction = func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return a.complete(cmd, "all-hosts", prefix)
		case 1:
			return cobra.FixedCompletions([]string{"hostname", "user", "port", "description", "enabled"}, cobra.ShellCompDirectiveNoFileComp)(cmd, args, prefix)
		default:
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
	rename := command("rename OLD NEW", "Change the canonical name", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		return a.mutate(ctx, true, func(q store.Queries) error {
			h, found, err := q.HostBySSHName(ctx, args[1])
			if err != nil {
				return err
			}
			if found && h.Name == args[0] && args[1] != args[0] {
				if err := q.Alias(ctx, h.ID, args[1], false); err != nil {
					return err
				}
			}
			return q.SetHost(ctx, args[0], "name", args[1])
		})
	})
	remove := command("remove NAME", "Delete a host and its references", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.mutate(cmd.Context(), true, func(q store.Queries) error {
			return q.RemoveHost(cmd.Context(), args[0])
		})
	})
	a.positional(rename, "all-hosts")
	a.positional(remove, "all-hosts")

	root.AddCommand(add, list, show, set, rename, remove, a.aliasCommands())
	for _, name := range []string{"enable", "disable"} {
		cmd := command(name+" NAME", name+" a host in inventory and SSH", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
			return a.mutate(cmd.Context(), true, func(q store.Queries) error {
				return q.SetHost(cmd.Context(), args[0], "enabled", strconv.FormatBool(name == "enable"))
			})
		})
		a.positional(cmd, "all-hosts")
		root.AddCommand(cmd)
	}
	return root
}

func (a *app) aliasCommands() *cobra.Command {
	root := &cobra.Command{Use: "alias", Short: "Manage additional SSH names"}
	for _, name := range []string{"add", "remove"} {
		cmd := command(name+" NAME ALIAS", name+" an SSH alias", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			return a.mutate(ctx, true, func(q store.Queries) error {
				h, err := q.Host(ctx, args[0])
				if err != nil {
					return err
				}
				return q.Alias(ctx, h.ID, args[1], name == "add")
			})
		})
		a.positional(cmd, "all-hosts")
		if name == "remove" {
			cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
				if len(args) == 0 {
					return a.complete(cmd, "all-hosts", prefix)
				}
				if len(args) == 1 {
					var aliases []string
					err := a.withDB(cmd.Context(), store.ReadOnly, func(s *store.Store) error {
						h, err := s.HostDetails(cmd.Context(), args[0])
						aliases = h.Aliases
						return err
					})
					if err == nil {
						return matching(aliases, prefix), cobra.ShellCompDirectiveNoFileComp
					}
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
		root.AddCommand(cmd)
	}
	return root
}
