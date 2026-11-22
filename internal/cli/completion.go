package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"hostdb/internal/store"
)

func (a *app) completionCommands(root *cobra.Command) *cobra.Command {
	completion := &cobra.Command{Use: "completion", Short: "Provide names and Zsh completion"}
	for _, kind := range []string{"hosts", "groups", "targets", "ssh-aliases"} {
		completion.AddCommand(command(kind, "Print "+kind+" one per line", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
			return a.withDB(cmd.Context(), store.ReadOnly, func(s *store.Store) error {
				names, err := s.Names(cmd.Context(), kind)
				if err != nil {
					return err
				}
				return printNames(cmd.OutOrStdout(), names)
			})
		}))
	}
	completion.AddCommand(command("zsh", "Generate the Zsh completion script", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		return root.GenZshCompletion(cmd.OutOrStdout())
	}))
	return completion
}

func (a *app) positional(cmd *cobra.Command, kinds ...string) {
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) >= len(kinds) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.complete(cmd, kinds[len(args)], prefix)
	}
}

func (a *app) complete(cmd *cobra.Command, kind, prefix string) ([]string, cobra.ShellCompDirective) {
	var names []string
	err := a.withDB(cmd.Context(), store.ReadOnly, func(s *store.Store) error {
		var err error
		names, err = s.Names(cmd.Context(), kind)
		return err
	})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return matching(names, prefix), cobra.ShellCompDirectiveNoFileComp
}

func matching(names []string, prefix string) []string {
	var matches []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			matches = append(matches, n)
		}
	}
	return matches
}
