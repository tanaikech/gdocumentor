package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tanaikech/gdocumentor/internal/cli"
)

// Version can be injected at build time via -ldflags "-X 'main.Version=v1.0.1'".
var Version = ""

func main() {
	if v := strings.TrimPrefix(strings.TrimSpace(Version), "v"); v != "" {
		cli.Version = v
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
