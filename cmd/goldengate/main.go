// Command goldengate is the local CLI and TUI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/OWNER/goldengate/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cli.New(cli.DefaultDeps()).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
