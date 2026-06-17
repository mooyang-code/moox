package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	transporteventbus "github.com/mooyang-code/moox/modules/storage/internal/infra/eventbus"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	_ "github.com/mooyang-code/moox/modules/storage/internal/infra/transport/nats"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/services/access"
	primarysvc "github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	if metadataInitRequested(os.Args) {
		if err := initMetadataSchema(context.Background(), configPathFromArgs(os.Args)); err != nil {
			log.Errorf("初始化 metadata schema 失败: %v", err)
			os.Exit(1)
		}
		log.Infof("metadata schema 初始化完成")
		return
	}

	// 清除unix域套接字文件，避免内部使用unix域套接字的服务启动失败
	clearSocketFiles()

	// 创建trpc服务器
	s := trpc.NewServer()

	// 量化金融数据协议服务。当前实现提供真实的文件型读写路径，用于承接
	// Space/Subject/DataSet/View 等新概念和 CSV 验收数据。
	opts := storageOptions()
	storageService := storagesvc.NewServiceWithOptions(opts)
	primaryService := primarysvc.NewService(primarysvc.Options{
		Root:       opts.Root,
		PebblePath: opts.PebblePath,
	})
	if err := storageService.InitViewBuilder(); err != nil {
		log.Errorf("初始化 ViewBuilder 失败: %v", err)
		os.Exit(1)
	}
	pb.RegisterMetadataServiceService(s, storageService)
	pb.RegisterDataServiceService(s, storageService)
	pb.RegisterQueryServiceService(s, storageService)
	pb.RegisterPrimaryStoreServiceService(s, primaryService)
	timer.RegisterScheduler("viewBuilderSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.storage.view.timer"), view.HandleSchedule)

	// 启动trpc服务器
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}

func storageRoot() string {
	return storageOptions().Root
}

func storageOptions() storagesvc.Options {
	configPath := configPathFromArgs(os.Args)
	opts := loadStorageOptions(configPath)
	if cfg, ok := loadStorageConfig(configPath); ok {
		events, err := newRowsChangedBus(context.Background(), cfg.Storage.EventBus)
		if err != nil {
			log.Errorf("初始化 storage eventbus 失败: %v", err)
			os.Exit(1)
		}
		opts.Events = events
	}
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		opts.Root = root
	}
	return opts
}

func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-conf=") {
			return strings.TrimPrefix(arg, "-conf=")
		}
		if strings.HasPrefix(arg, "--conf=") {
			return strings.TrimPrefix(arg, "--conf=")
		}
		if (arg == "-conf" || arg == "--conf") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("STORAGE_CONFIG_FILE"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "trpc_go.yaml")
	}
	return filepath.Join("config", "trpc_go.yaml")
}

func metadataInitRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-init-metadata" || arg == "--init-metadata" {
			return true
		}
	}
	return false
}

func initMetadataSchema(ctx context.Context, configPath string) error {
	opts := loadStorageOptions(configPath)
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		opts.Root = root
	}
	root := opts.Root
	if root == "" {
		root = "var/storage"
	}
	metadataPath := opts.MetadataPath
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	schemaPath := metadataSchemaPath(configPath)
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		return err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{Path: metadataPath, SchemaPath: schemaPath})
	if err != nil {
		return err
	}
	defer store.Close()
	return store.InitSchema(ctx)
}

func metadataSchemaPath(configPath string) string {
	if path := os.Getenv("STORAGE_SCHEMA_FILE"); path != "" {
		return path
	}
	candidates := []string{}
	if configPath != "" {
		candidates = append(candidates, filepath.Clean(filepath.Join(filepath.Dir(configPath), "..", "schema", "storage_metadata.sql")))
	}
	candidates = append(candidates,
		filepath.Join("schema", "storage_metadata.sql"),
		filepath.Join("modules", "storage", "schema", "storage_metadata.sql"),
	)
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func loadStorageRoot(configPath string) string {
	return loadStorageOptions(configPath).Root
}

func loadStorageOptions(configPath string) storagesvc.Options {
	cfg, ok := loadStorageConfig(configPath)
	if !ok {
		return storagesvc.Options{}
	}
	return storagesvc.Options{
		Root:               cfg.Storage.Root,
		MetadataPath:       cfg.Storage.Metadata.Path,
		PebblePath:         cfg.Storage.Devices.PebblePath,
		DuckDBPath:         cfg.Storage.Devices.DuckDBPath,
		BlevePath:          cfg.Storage.Devices.BlevePath,
		PrimaryServiceName: cfg.Storage.Primary.ServiceName,
	}
}

func loadStorageConfig(configPath string) (storageconfig.RuntimeConfig, bool) {
	var cfg storageconfig.RuntimeConfig
	if configPath == "" {
		return cfg, false
	}
	dir := filepath.Dir(configPath)
	file := filepath.Base(configPath)
	if err := storageconfig.NewConfigLoader(dir).LoadConfigWithDefaults(file, &cfg, cfg.ApplyDefaults); err != nil {
		log.Warnf("加载 storage 配置失败，使用默认目录: %v", err)
		return cfg, false
	}
	return cfg, true
}

func newRowsChangedBus(ctx context.Context, cfg storageconfig.StorageEventBus) (coreeventbus.Bus, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "memory":
		return coreeventbus.NewMemoryBus(), nil
	case "nats":
		subject := cfg.RowsChangedSubject
		if subject == "" {
			subject = transporteventbus.DefaultRowsChangedSubject
		}
		producer, err := transport.NewProducer(transport.ProducerKindNATS, transport.ProducerOptions{
			ServerURL:      cfg.NATSURL,
			ConnectTimeout: 10 * time.Second,
			StreamName:     cfg.StreamName,
			StreamSubjects: []string{subject},
		})
		if err != nil {
			return nil, err
		}
		if err := producer.Connect(ctx); err != nil {
			return nil, err
		}
		return transporteventbus.NewProducerBus(producer, subject), nil
	default:
		return nil, fmt.Errorf("unsupported storage eventbus type %s", cfg.Type)
	}
}

func clearSocketFiles() {
	files, err := filepath.Glob("./*")
	if err != nil {
		log.Errorf("读取目录失败: %v", err)
		return
	}

	for _, file := range files {
		baseFile := filepath.Base(file)
		if strings.HasPrefix(baseFile, "0.0.0.0") || strings.HasPrefix(baseFile, "127.0.0.1") {
			if err := os.Remove(file); err != nil {
				log.Errorf("删除文件 %s 失败: %v", file, err)
			}
		}
	}
}
