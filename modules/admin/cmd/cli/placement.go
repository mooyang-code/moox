package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func isTopologyRecoveryCommand(args []string) bool {
	return len(args) > 1 && (args[1] == "host" || args[1] == "placement")
}

func runTopologyRecoveryCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 || (args[0] != "host" && args[0] != "placement") || args[1] != "set-status" {
		return errors.New("expected host set-status or placement set-status")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet(args[0]+" set-status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db-path", defaultInitDBPath, "existing Admin SQLite database; no schema initialization")
	controlHostID := fs.String("control-host-id", "", "canonical protected control host ID")
	hostID := fs.String("host-id", "", "canonical host ID")
	componentID := ""
	if args[0] == "placement" {
		fs.StringVar(&componentID, "component-id", "", "canonical deployed component ID")
	}
	status := fs.String("status", "", "enabled or disabled")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || !servicecatalog.ValidHostID(*hostID) || !servicecatalog.ValidHostID(*controlHostID) || (*status != servicecatalog.Enabled && *status != servicecatalog.Disabled) {
		return errors.New("recovery requires --control-host-id, --host-id and --status enabled|disabled, with no positional arguments")
	}
	if args[0] == "placement" {
		catalog, err := servicecatalog.LoadEmbedded()
		if err != nil {
			return err
		}
		if _, ok := catalog.Component(componentID); !ok {
			return errors.New("placement recovery requires a catalog --component-id")
		}
	}
	if err := validateExistingAdminDatabase(*dbPath); err != nil {
		return err
	}
	db, err := openAdminCLIDB(*dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, *controlHostID)
	if err != nil {
		return err
	}
	if args[0] == "host" {
		err = dao.SetHostStatus(context.Background(), *hostID, *status)
	} else {
		err = dao.SetPlacementStatus(context.Background(), *hostID, componentID, *status)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]string{"status": "ok", "host_id": *hostID, "component_id": componentID, "deployment_status": *status})
}
