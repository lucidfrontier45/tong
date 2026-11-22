// Command tong synchronizes files between local directories and Amazon S3.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/lucidfrontier45/tong/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
