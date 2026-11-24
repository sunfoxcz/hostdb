package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"hostdb/internal/inventory"
	"hostdb/internal/store"
)

func seed(b *testing.B, n int) (context.Context, *store.Store, string) {
	b.Helper()
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "bench.db")
	_, err := store.Init(ctx, path)
	need(b, err)
	s, err := store.Open(ctx, path, store.Write)
	need(b, err)
	err = s.Transaction(ctx, func(q store.Queries) error {
		for _, group := range []string{"web", "web_nodes"} {
			if err := q.AddGroup(ctx, group, ""); err != nil {
				return err
			}
		}
		if err := q.Link(ctx, "web", "web_nodes", true, true); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("node-%05d", i)
			_, err := q.CreateHost(ctx, store.Host{Name: name, Hostname: name + ".example", Port: 22, Enabled: true})
			if err != nil {
				return err
			}
			if err := q.Link(ctx, "web_nodes", name, false, true); err != nil {
				return err
			}
		}
		return nil
	})
	need(b, err)
	need(b, s.Close())
	reader, err := store.Open(ctx, path, store.ReadOnly)
	need(b, err)
	b.Cleanup(func() { reader.Close() })
	return ctx, reader, path
}

func BenchmarkSQL(b *testing.B) {
	ctx, reader, _ := seed(b, 1000)
	b.Run("host", func(b *testing.B) {
		for b.Loop() {
			_, err := reader.Host(ctx, "node-00500")
			need(b, err)
		}
	})
	b.Run("group", func(b *testing.B) {
		for b.Loop() {
			_, err := reader.Group(ctx, "web_nodes")
			need(b, err)
		}
	})
}

func BenchmarkInventory(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			ctx, reader, _ := seed(b, count)
			encoder := json.NewEncoder(io.Discard)
			for b.Loop() {
				need(b, reader.Transaction(ctx, func(q store.Queries) error {
					data, err := inventory.List(ctx, q)
					if err != nil {
						return err
					}
					return encoder.Encode(data)
				}))
			}
		})
	}
}

func BenchmarkCLI(b *testing.B) {
	exe := binary(b)
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	need(b, err)
	defer out.Close()
	bench := func(b *testing.B, path string, args ...string) {
		b.Helper()
		for b.Loop() {
			cmd := exec.Command(exe, append([]string{"--db", path}, args...)...)
			cmd.Stdout, cmd.Stderr = out, os.Stderr
			need(b, cmd.Run())
		}
	}
	_, _, path := seed(b, 1000)
	b.Run("host", func(b *testing.B) {
		bench(b, path, "host", "show", "node-00500")
	})
	b.Run("group", func(b *testing.B) {
		bench(b, path, "group", "show", "web")
	})
	b.Run("inventory", func(b *testing.B) {
		for _, count := range []int{100, 1000, 10000} {
			b.Run(fmt.Sprint(count), func(b *testing.B) {
				_, _, path := seed(b, count)
				bench(b, path, "--list")
			})
		}
	})
}
