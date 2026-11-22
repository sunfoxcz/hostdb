package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hostdb/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.New().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hostdb:", err)
		os.Exit(1)
	}
}
