// Command nexr is a command-line tool for Sonatype Nexus Repository 3.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yand3r3d3v/nexr/internal/cli/cmdutil"
	"github.com/yand3r3d3v/nexr/internal/cli/root"
	"github.com/yand3r3d3v/nexr/internal/output"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		// After the first signal, restore the default behaviour so that a
		// second Ctrl-C terminates immediately.
		<-ctx.Done()
		stop()
	}()
	code := root.Main(ctx, os.Args[1:], cmdutil.New(output.System()))
	stop()
	os.Exit(code)
}
