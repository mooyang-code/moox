package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

type hostBundleOptions struct {
	hostID, controlHostID                 string
	dbPath, masterFile, pkiDir, outputDir string
}

func isHostBundleCommand(args []string) bool { return len(args) > 1 && args[1] == "host-bundle" }

func runHostBundleCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "host-bundle" {
		return errors.New("expected host-bundle command")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("host-bundle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts hostBundleOptions
	fs.StringVar(&opts.hostID, "host-id", "", "registered target host ID")
	fs.StringVar(&opts.controlHostID, "control-host-id", "", "protected control host ID")
	fs.StringVar(&opts.dbPath, "db-path", defaultInitDBPath, "existing Admin SQLite database")
	fs.StringVar(&opts.masterFile, "encryption-key-file", "", "existing persistent 0600 Admin encryption key")
	fs.StringVar(&opts.pkiDir, "pki-dir", "", "existing persistent 0700 control CA directory")
	fs.StringVar(&opts.outputDir, "output-dir", "", "0700 parent directory for a complete new host bundle")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || !servicecatalog.ValidHostID(opts.hostID) || !servicecatalog.ValidHostID(opts.controlHostID) {
		return errors.New("host-bundle requires canonical --host-id and --control-host-id, with no positional arguments")
	}
	for _, path := range []*string{&opts.dbPath, &opts.masterFile, &opts.pkiDir, &opts.outputDir} {
		if *path == "" || *path != strings.TrimSpace(*path) || strings.ContainsAny(*path, "\x00\r\n?#") || strings.HasPrefix(*path, "file:") {
			return errors.New("host-bundle requires ordinary --db-path, --encryption-key-file, --pki-dir and --output-dir paths")
		}
		abs, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		*path = abs
	}
	if opts.dbPath == opts.masterFile {
		return errors.New("database and encryption key must use different files")
	}
	return exportHostBundle(context.Background(), opts, stdout)
}

func exportHostBundle(ctx context.Context, opts hostBundleOptions, stdout io.Writer) error {
	if err := validateExistingAdminDatabase(opts.dbPath); err != nil {
		return err
	}
	masterRoot, err := privatefiles.OpenRoot(filepath.Dir(opts.masterFile))
	if err != nil {
		return err
	}
	defer masterRoot.Close()
	// Use the same OS lock as bootstrap. Live Admin writes remain coordinated
	// through SQLite's writer reservation below.
	lock, err := privatefiles.Lock(masterRoot, ".bootstrap-"+filepath.Base(opts.masterFile)+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	raw, err := privatefiles.ReadAt(masterRoot, filepath.Base(opts.masterFile), 8192)
	if err != nil {
		return err
	}
	master := strings.TrimSpace(string(raw))
	if len(master) < 32 || strings.ContainsAny(master, "\x00\r\n") {
		return errors.New("Admin encryption key must contain at least 32 characters on one line")
	}
	ca, err := pki.Open(opts.pkiDir)
	if err != nil {
		return err
	}
	defer ca.Close()
	info, err := ca.Info()
	if err != nil {
		return err
	}
	db, err := openAdminCLIDB(opts.dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	material := hostBundleMaterial{caInfo: info}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE t_hosts SET c_host_id = c_host_id WHERE 0").Error; err != nil {
			return err
		}
		dao, err := sysdeploy.NewTopologyDAO(tx, opts.controlHostID)
		if err != nil {
			return err
		}
		hosts, placements, err := dao.Read(ctx)
		if err != nil {
			return err
		}
		target := slices.IndexFunc(hosts, func(h sysdeploy.HostRecord) bool { return h.HostID == opts.hostID })
		control := slices.IndexFunc(hosts, func(h sysdeploy.HostRecord) bool { return h.HostID == opts.controlHostID })
		if target < 0 || control < 0 {
			return errors.New("host-bundle requires registered target and control hosts; sync placements first")
		}
		h := hosts[target]
		material.host = bootstrapHost{HostID: h.HostID, Address: h.Address, PrivateAddress: h.PrivateAddress}
		material.control = bootstrapHost{HostID: hosts[control].HostID, Address: hosts[control].Address}
		for _, placement := range placements {
			if placement.HostID == h.HostID {
				material.host.Components = append(material.host.Components, placement.ComponentID)
			}
		}
		// Validate the persisted fleet before provisioning any new identity.
		if _, err := dao.Compile(ctx, h.HostID); err != nil {
			return err
		}
		store, err := keys.NewStore(tx, master)
		if err != nil {
			return err
		}
		for _, caller := range hostSigningCallers(material.host, false) {
			key, err := store.Ensure(ctx, caller)
			if err != nil {
				return err
			}
			material.signing = append(material.signing, key)
		}
		material.access, err = hostAccessVerification(ctx, store, material.host)
		if err != nil {
			return err
		}
		_, snapshot, err := dao.CompileSnapshot(ctx, h.HostID, master)
		if err != nil {
			return err
		}
		material.expectedHash = snapshot.Hash
		return nil
	})
	if err != nil {
		return err
	}
	result, err := publishHostBundle(opts.outputDir, material, ca)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func hostAccessVerification(ctx context.Context, store *keys.Store, host bootstrapHost) ([]keys.VerificationKey, error) {
	if !slices.Contains(host.Components, "access") {
		return nil, nil
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	for _, principal := range catalog.Principals {
		if _, err := store.Ensure(ctx, principal.ID); err != nil {
			return nil, err
		}
	}
	return store.ExternalVerification(ctx)
}
