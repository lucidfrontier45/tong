// Command tong synchronizes files between local directories and Amazon S3.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lucidfrontier45/tong/internal/cli"
)

func main() {
	// SIGTERM is what systemd, Docker and Kubernetes send, so tong has to
	// cancel its transfers on it as well.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
