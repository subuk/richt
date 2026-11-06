package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/subuk/richt/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The first signal asks active operations to stop; restore the default
	// behavior so a second signal can terminate an unresponsive operation.
	go func() { <-ctx.Done(); stop() }()
	code := cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
