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
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"gorm.io/gorm"
)

type hostBundleOptions struct {
	hostID, controlHostID                 string
	dbPath, masterFile, pkiDir, outputDir string
	// current 是目标机上现有私钥的公钥；rotate 显式要求换掉主机签名密钥。
	current currentKeys
	rotate  bool
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
	registerCurrentKeyFlags(fs, &opts.current)
	fs.BoolVar(&opts.rotate, "rotate-sign-key", false, "换掉这台主机的签名密钥（旧私钥立即作废，这台主机的全部组件要重启）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := opts.current.validate(); err != nil {
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

// registerCurrentKeyFlags 注册“目标机上现有私钥的公钥”两个参数，bootstrap 与 host-bundle 共用。
func registerCurrentKeyFlags(fs *flag.FlagSet, current *currentKeys) {
	fs.StringVar(&current.host, "current-sign-public-key", "", "目标机上现有主机签名私钥的公钥（base64）；和登记的一致就沿用，不换密钥")
	fs.StringVar(&current.scfCollector, "current-scf-collector-public-key", "", "目标机上现有 scf-collector 私钥的公钥（base64）")
}

func (c currentKeys) validate() error {
	for _, value := range []string{c.host, c.scfCollector} {
		if value == "" {
			continue
		}
		if _, err := gatewayauth.ParsePublicKey(value); err != nil {
			return keys.ErrInvalidPublicKey
		}
	}
	return nil
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
	// 和 bootstrap 用同一把进程间文件锁；运行中的 Admin 由下面的 SQLite 写锁协调。
	lock, err := privatefiles.Lock(masterRoot, ".bootstrap-"+filepath.Base(opts.masterFile)+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
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
		hosts, deployments, err := dao.Read(ctx)
		if err != nil {
			return err
		}
		target := slices.IndexFunc(hosts, func(h sysdeploy.HostRecord) bool { return h.HostID == opts.hostID })
		control := slices.IndexFunc(hosts, func(h sysdeploy.HostRecord) bool { return h.HostID == opts.controlHostID })
		if target < 0 || control < 0 {
			return errors.New("host-bundle requires registered target and control hosts; sync deployments first")
		}
		h := hosts[target]
		reachability, _ := servicecatalog.ParseReachability(h.Reachability)
		material.host = hostbundle.Host{HostID: h.HostID, Address: h.Address, PrivateAddress: h.PrivateAddress, Reachability: reachability}
		material.control = hostbundle.Host{HostID: hosts[control].HostID, Address: hosts[control].Address}
		for _, deployment := range deployments {
			if deployment.HostID == h.HostID {
				material.host.Components = append(material.host.Components, deployment.ComponentID)
			}
		}
		// 先确认库里的整套拓扑合法，再动签名密钥。
		if _, err := dao.Compile(ctx, h.HostID); err != nil {
			return err
		}
		store, err := keys.NewStore(tx)
		if err != nil {
			return err
		}
		material.keys, err = ensureSigningKeys(ctx, store, material.host, opts.current, opts.rotate)
		return err
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
