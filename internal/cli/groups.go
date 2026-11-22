package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"hostdb/internal/store"
)

func (a *app) groupCommands() *cobra.Command {
	root := &cobra.Command{Use: "group", Short: "Manage Ansible groups and child relationships"}
	var description string
	add := command("add NAME", "Create a group", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.mutate(cmd.Context(), false, func(q store.Queries) error {
			return q.AddGroup(cmd.Context(), args[0], description)
		})
	})
	add.Flags().StringVar(&description, "description", "", "Group description")
	set := command("set NAME DESCRIPTION", "Update a group description", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		return a.mutate(cmd.Context(), false, func(q store.Queries) error {
			return q.DescribeGroup(cmd.Context(), args[0], args[1])
		})
	})
	remove := command("remove NAME", "Delete a group and its relationships", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.mutate(cmd.Context(), false, func(q store.Queries) error {
			return q.RemoveGroup(cmd.Context(), args[0])
		})
	})
	var format string
	list := command("list", "List groups", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		return a.withDB(cmd.Context(), store.ReadOnly, func(s *store.Store) error {
			out := cmd.OutOrStdout()
			if format == "names" {
				names, err := s.Names(cmd.Context(), "groups")
				if err != nil {
					return err
				}
				return printNames(out, names)
			}
			if format != "table" && format != "json" {
				return fmt.Errorf("unknown output format %q", format)
			}
			groups, err := s.Groups(cmd.Context())
			if err != nil {
				return err
			}
			if format == "json" {
				return prettyJSON(out, groups)
			}
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "NAME\tDESCRIPTION")
			for _, group := range groups {
				fmt.Fprintf(table, "%s\t%s\n", group.Name, group.Description)
			}
			return table.Flush()
		})
	})
	list.Flags().StringVar(&format, "format", "table", "Output format (table, json, names)")
	var showFormat string
	show := command("show NAME", "Show direct members, children and parents", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		var group store.Group
		err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
			return s.Transaction(ctx, func(q store.Queries) error {
				var err error
				group, err = q.GroupDetails(ctx, args[0])
				return err
			})
		})
		if err != nil {
			return err
		}
		switch showFormat {
		case "json":
			return prettyJSON(cmd.OutOrStdout(), group)
		case "text":
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n  description: %s\n  hosts: %s\n  children: %s\n  parents: %s\n",
				group.Name, group.Description, strings.Join(group.Hosts, ", "), strings.Join(group.Children, ", "), strings.Join(group.Parents, ", "))
			return err
		default:
			return fmt.Errorf("unknown output format %q", showFormat)
		}
	})
	show.Flags().StringVar(&showFormat, "format", "text", "Output format (text, json)")
	tree := command("tree NAME", "Show the hierarchy under a group", cobra.ExactArgs(1), a.groupTree)
	for _, cmd := range []*cobra.Command{set, remove, show, tree} {
		a.positional(cmd, "groups")
	}
	root.AddCommand(add, set, remove, list, show, tree, a.groupLinks(false), a.groupLinks(true))
	return root
}

func (a *app) groupTree(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	var edges []store.Pair
	err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error {
		return s.Transaction(ctx, func(q store.Queries) error {
			if _, err := q.Group(ctx, args[0]); err != nil {
				return err
			}
			var err error
			edges, err = q.Children(ctx)
			return err
		})
	})
	if err != nil {
		return err
	}
	children := make(map[string][]string)
	for _, edge := range edges {
		children[edge.First] = append(children[edge.First], edge.Second)
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintln(out, args[0]); err != nil {
		return err
	}
	return drawTree(out, children, map[string]bool{args[0]: true}, args[0], "")
}

func drawTree(out io.Writer, children map[string][]string, visited map[string]bool, node, prefix string) error {
	for i, child := range children[node] {
		connector, spacer := "├── ", "│   "
		if i == len(children[node])-1 {
			connector, spacer = "└── ", "    "
		}
		label := child
		if visited[child] {
			label += " (shared)"
		}
		if _, err := fmt.Fprintln(out, prefix+connector+label); err != nil {
			return err
		}
		if !visited[child] {
			visited[child] = true
			if err := drawTree(out, children, visited, child, prefix+spacer); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *app) groupLinks(child bool) *cobra.Command {
	name, subject, completion := "host", "HOST", "all-hosts"
	if child {
		name, subject, completion = "child", "CHILD", "groups"
	}
	root := &cobra.Command{Use: name, Short: "Manage direct " + name + " relationships"}
	for _, verb := range []string{"add", "remove"} {
		cmd := command(verb+" GROUP "+subject, verb+" a direct "+name+" relationship", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
			return a.mutate(cmd.Context(), false, func(q store.Queries) error {
				return q.Link(cmd.Context(), args[0], args[1], child, verb == "add")
			})
		})
		a.positional(cmd, "groups", completion)
		root.AddCommand(cmd)
	}
	return root
}
