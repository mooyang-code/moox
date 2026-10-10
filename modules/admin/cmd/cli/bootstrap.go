package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// moox-admin-cli bootstrap 是首次部署的离线初始化（设计文档 3.12 第 2 步），不依赖任何运行中的服务，
// 可以重复执行：已有的表、CA、密钥都复用，不会重建。依次完成：
//  1. 执行 admin.sql 建表；
//  2. 生成 MooX 私有 CA；
//  3. 按部署表写入全部主机和部署（与 SyncHostPlacements 同一套校验）；
//  4. 生成全部调用方密钥并存入 Admin 密钥表；
//  5. 为 control 签发主机网关证书，并导出 control 主机网关需要的 CA 与证书。control 的主机网关直连
//     本机的网关控制，不需要调用方密钥。

// BootstrapSpec 是 moox-cli 按 moox.toml 渲染的部署表。
type BootstrapSpec struct {
	Hosts []BootstrapHost `json:"hosts"`
}

// BootstrapHost 是部署表中的一台主机。
type BootstrapHost struct {
	ID             string   `json:"id"`
	Address        string   `json:"address"`
	PrivateAddress string   `json:"private_address"`
	Region         string   `json:"region"`
	Components     []string `json:"components"`
}

func isBootstrapCommand(args []string) bool { return len(args) > 1 && args[1] == "bootstrap" }

func runBootstrapCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, keyFile, pkiDir, specPath, outDir := defaultInitDBPath, "", defaultPKIDir, "", ""
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite 数据库路径")
	fs.StringVar(&keyFile, "encryption-key-file", "", "0600 的 Admin 加密密钥文件（不存在时生成）")
	fs.StringVar(&pkiDir, "pki-dir", pkiDir, "MooX 私有 CA 目录")
	fs.StringVar(&specPath, "spec", "", "部署表 JSON（由 moox-cli 按 moox.toml 生成）")
	fs.StringVar(&outDir, "out-dir", "", "control 主机网关文件的输出根目录（写入 certs/ 与 secrets/）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if specPath == "" || outDir == "" {
		return errors.New("bootstrap 需要 --spec 和 --out-dir")
	}
	spec, err := loadBootstrapSpec(specPath)
	if err != nil {
		return err
	}
	if err := loadCLIKey(dbPath, keyFile); err != nil {
		return err
	}
	result, err := bootstrap(trpc.BackgroundContext(), dbPath, pkiDir, outDir, spec, time.Now())
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func loadBootstrapSpec(path string) (BootstrapSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return BootstrapSpec{}, fmt.Errorf("读取部署表: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec BootstrapSpec
	if err := decoder.Decode(&spec); err != nil {
		return BootstrapSpec{}, fmt.Errorf("解析部署表: %w", err)
	}
	if len(spec.Hosts) == 0 {
		return BootstrapSpec{}, errors.New("部署表没有主机")
	}
	hasControl := false
	for _, host := range spec.Hosts {
		if host.ID == servicecatalog.ControlHostID {
			hasControl = true
		}
	}
	if !hasControl {
		return BootstrapSpec{}, errors.New("部署表必须包含 control 主机")
	}
	return spec, nil
}

type bootstrapResult struct {
	Status      string   `json:"status"`
	CACreated   bool     `json:"ca_created"`
	HostsSynced []string `json:"hosts_synced"`
	KeysCreated []string `json:"keys_created"`
	OutDir      string   `json:"out_dir"`
}

func bootstrap(ctx context.Context, dbPath, pkiDir, outDir string, spec BootstrapSpec, now time.Time) (bootstrapResult, error) {
	if err := ensureAdminSchema(dbPath); err != nil {
		return bootstrapResult{}, err
	}
	caCreated, _, err := pki.EnsureCA(pkiDir, now)
	if err != nil {
		return bootstrapResult{}, err
	}
	db, err := openAdminCLIDBWithPragmas(dbPath)
	if err != nil {
		return bootstrapResult{}, err
	}
	defer closeAdminCLIDB(db)

	// control 先写：受保护的组件必须部署在 control。
	hosts := append([]BootstrapHost(nil), spec.Hosts...)
	sort.SliceStable(hosts, func(i, j int) bool {
		return hosts[i].ID == servicecatalog.ControlHostID && hosts[j].ID != servicecatalog.ControlHostID
	})
	result := bootstrapResult{Status: "ok", CACreated: caCreated, OutDir: outDir}
	var control BootstrapHost
	// 所有主机在同一个事务里写入：第 3 台主机违反目录规则时，前面的主机不留在库里，库不会停在只写了一半的状态。
	if err := db.Transaction(func(tx *gorm.DB) error {
		placements := placement.NewService(tx, nil)
		for _, host := range hosts {
			if _, err := placements.SyncHostPlacements(ctx, placement.HostSpec{
				HostID: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
			}, host.Components); err != nil {
				return fmt.Errorf("写入主机 %s: %w", host.ID, err)
			}
			result.HostsSynced = append(result.HostsSynced, host.ID)
			if host.ID == servicecatalog.ControlHostID {
				control = host
			}
		}
		return nil
	}); err != nil {
		return bootstrapResult{}, err
	}

	keyService := keys.NewService(secretdao.NewSecretDAO(db))
	for _, identity := range bootstrapIdentities(spec) {
		_, created, err := keyService.Ensure(ctx, keys.CategoryCaller, identity)
		if err != nil {
			return bootstrapResult{}, err
		}
		if created {
			result.KeysCreated = append(result.KeysCreated, identity)
		}
	}
	for _, principal := range servicecatalog.Default().Principals {
		_, created, err := keyService.Ensure(ctx, keys.CategoryPrincipal, principal.ID)
		if err != nil {
			return bootstrapResult{}, err
		}
		if created {
			result.KeysCreated = append(result.KeysCreated, principal.ID)
		}
	}

	if _, err := issueHostCertificate(pkiDir, control.ID, []string{control.Address, control.PrivateAddress},
		filepath.Join(outDir, "certs", "host-gateway"), now); err != nil {
		return bootstrapResult{}, err
	}
	if err := exportCA(pkiDir, filepath.Join(outDir, "certs", "moox-ca.crt")); err != nil {
		return bootstrapResult{}, err
	}
	return result, nil
}

// bootstrapIdentities 返回初始化时生成密钥的全部内部调用方身份：每个组件、console、moox-cli，
// 以及 control 之外每台主机的 host-gateway@<主机>。
func bootstrapIdentities(spec BootstrapSpec) []string {
	catalog := servicecatalog.Default()
	identities := map[string]bool{}
	for _, component := range catalog.Components {
		if component.ID == servicecatalog.HostGatewayCaller {
			continue
		}
		identities[component.ID] = true
	}
	for _, caller := range catalog.Callers {
		identities[caller.ID] = true
	}
	for _, host := range spec.Hosts {
		if host.ID != servicecatalog.ControlHostID {
			identities[servicecatalog.HostGatewayIdentity(host.ID)] = true
		}
	}
	out := make([]string, 0, len(identities))
	for identity := range identities {
		out = append(out, identity)
	}
	sort.Strings(out)
	return out
}

func openAdminCLIDBWithPragmas(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	return db, nil
}
