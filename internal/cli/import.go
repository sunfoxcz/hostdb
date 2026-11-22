package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"hostdb/internal/importer"
	"hostdb/internal/store"
)

func (a *app) importCommands() *cobra.Command {
	root := &cobra.Command{Use: "import", Short: "Preview or import existing inventory and SSH files"}
	for _, kind := range []string{"ansible-ini", "ssh-config"} {
		var dryRun, allowShared bool
		var mappings []string
		cmd := command(kind+" FILE", "Import "+kind, cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			plan, err := parseFile(args[0], kind)
			if plan != nil {
				for _, warning := range plan.Warnings {
					fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", warning)
				}
			}
			if err != nil {
				return err
			}
			opts, err := importOptions(mappings, allowShared)
			if err != nil {
				return err
			}
			var changes []string
			apply := func(q store.Queries) error {
				var err error
				changes, err = plan.Apply(ctx, q, opts)
				return err
			}
			if dryRun {
				err = a.dryRun(ctx, apply)
			} else {
				err = a.mutate(ctx, true, apply)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				if _, err := fmt.Fprintln(out, "Dry run"); err != nil {
					return err
				}
			}
			if len(changes) == 0 {
				_, err := fmt.Fprintln(out, "No changes.")
				return err
			}
			return printNames(out, changes)
		})
		cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Plan against a private snapshot without changing the database")
		cmd.Flags().BoolVar(&allowShared, "allow-shared-address", false, "Allow distinct hosts to use the same address")
		if kind == "ssh-config" {
			cmd.Flags().StringArrayVar(&mappings, "map", nil, "Explicit SSH name to canonical name mapping (SSH=CANONICAL, repeatable)")
		}
		root.AddCommand(cmd)
	}
	return root
}

func parseFile(path, kind string) (*importer.Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	parse := func(r io.Reader) (*importer.Plan, error) {
		if kind == "ssh-config" {
			return importer.SSH(r)
		}
		return importer.AnsibleINI(r)
	}
	p, err := parse(f)
	if err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func importOptions(mappings []string, shared bool) (importer.Options, error) {
	opts := importer.Options{Map: map[string]string{}, AllowSharedAddress: shared}
	for _, mapping := range mappings {
		alias, canonical, ok := strings.Cut(mapping, "=")
		if !ok {
			return opts, fmt.Errorf("invalid --map %q; use SSH=CANONICAL", mapping)
		}
		if err := errors.Join(store.HostName(alias), store.CanonicalName(canonical)); err != nil {
			return opts, err
		}
		if previous, ok := opts.Map[alias]; ok && canonical != previous {
			return opts, fmt.Errorf("conflicting mappings for SSH name %q", alias)
		}
		opts.Map[alias] = canonical
	}
	return opts, nil
}

func (a *app) dryRun(ctx context.Context, apply func(store.Queries) error) error {
	dir, err := os.MkdirTemp("", "hostdb-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	snapshot := filepath.Join(dir, "snapshot.db")
	path, err := store.Path(a.dbPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := a.withDB(ctx, store.ReadOnly, func(s *store.Store) error { return s.Backup(ctx, snapshot) }); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	s, err := store.Open(ctx, snapshot, store.Initialize)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Transaction(ctx, apply)
}
