package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

var Version = "dev"

func main() {
	if len(os.Args) == 4 && os.Args[1] == "exec-component" {
		if err := unitruntime.RunComponentChild(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		// Private file contents and child output never form part of errors.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && args[0] == "version" {
		return json.NewEncoder(stdout).Encode(map[string]string{"version": Version, "catalog_sha256": unitruntime.CatalogSHA256()})
	}
	if len(args) == 0 {
		return errors.New("usage: moox-runtime start|stop|restart|pause|resume|healthcheck|status --plan PATH [--components ID,...]")
	}
	flags := flag.NewFlagSet("moox-runtime "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var plan, components string
	var options unitruntime.Options
	flags.StringVar(&plan, "plan", "", "private runtime.json in a physical release or its current view")
	flags.StringVar(&components, "components", "", "deployed component IDs; defaults to the entire unit")
	flags.BoolVar(&options.MaintenanceLockHeld, "maintenance-lock-held", false, "reuse the caller's inherited exclusive maintenance lock")
	flags.IntVar(&options.MaintenanceLockFD, "maintenance-lock-fd", 3, "inherited maintenance lock descriptor")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || plan == "" {
		return errors.New("runtime requires --plan and no positional arguments")
	}
	var ids []string
	if components != "" {
		ids = strings.Split(components, ",")
	}
	result, err := unitruntime.Execute(ctx, plan, args[0], ids, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
