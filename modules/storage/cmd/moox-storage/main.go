package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/services/access"
	"github.com/mooyang-code/moox/modules/storage/internal/services/archive"
	"github.com/mooyang-code/moox/modules/storage/internal/services/deriver"
	primarysvc "github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func init() {
	registerStorageFlags(flag.CommandLine)
}

func registerStorageFlags(flags *flag.FlagSet) {
	if flags == nil || flags.Lookup("storage-conf") != nil {
		return
	}
	flags.String("storage-conf", "", "storage business config path")
}

func main() {
	if metadataInitRequested(os.Args) {
		frameworkConfigPath := configPathFromArgs(os.Args)
		storageConfigPath := storageConfigPathFromArgs(os.Args, frameworkConfigPath)
		if err := initMetadataSchema(trpc.BackgroundContext(), frameworkConfigPath, storageConfigPath); err != nil {
			log.Errorf("初始化 metadata schema 失败: %v", err)
			os.Exit(1)
		}
		log.Infof("metadata schema 初始化完成")
		return
	}

	if metadataImportRequested(os.Args) {
		frameworkConfigPath := configPathFromArgs(os.Args)
		storageConfigPath := storageConfigPathFromArgs(os.Args, frameworkConfigPath)
		if err := importMetadataSeed(trpc.BackgroundContext(), frameworkConfigPath, storageConfigPath); err != nil {
			log.Errorf("导入 metadata seed 失败: %v", err)
			os.Exit(1)
		}
		return
	}

	// 清除unix域套接字文件，避免内部使用unix域套接字的服务启动失败
	clearSocketFiles()

	// 创建trpc服务器
	s := trpc.NewServer()

	frameworkConfigPath := configPathFromArgs(os.Args)
	storageConfigPath := storageConfigPathFromArgs(os.Args, frameworkConfigPath)
	cfg := loadRuntimeConfig(storageConfigPath)
	opts := storageOptionsFromConfig(cfg.Storage)
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		cfg.Storage.Root = root
		opts.Root = root
	}
	if needsRowsChangedBus(cfg.Storage) {
		embeddedEventBus, err := eventbus.StartEmbeddedServer(cfg.Storage.EventBus)
		if err != nil {
			log.Errorf("启动内嵌 storage eventbus 失败: %v", err)
			os.Exit(1)
		}
		if embeddedEventBus != nil {
			defer func() {
				if err := embeddedEventBus.Close(); err != nil {
					log.Errorf("关闭内嵌 storage eventbus 失败: %v", err)
				}
			}()
			log.Infof("Embedded storage eventbus initialized")
		}
		events, err := eventbus.NewRowsChangedBus(trpc.BackgroundContext(), cfg.Storage.EventBus)
		if err != nil {
			log.Errorf("初始化 storage eventbus 失败: %v", err)
			os.Exit(1)
		}
		opts.Events = events
	}

	var storageService *storagesvc.Service
	if shouldCreateStorageService(cfg.Storage) {
		storageService = storagesvc.NewServiceWithOptions(opts)
		defer func() {
			if err := storageService.Close(); err != nil {
				log.Errorf("关闭 storage service 失败: %v", err)
			}
		}()
	}
	var accessReader deriver.AccessReader
	if storageService != nil {
		accessReader = accessReaderForRuntime(cfg.Storage, storageService)
		storageService.SetViewFactReader(accessReader)
	}

	if cfg.Storage.HasRole("access") {
		if storageService == nil {
			log.Errorf("access role requires storage service")
			os.Exit(1)
		}
		pb.RegisterMetadataService(s, storageService)
		pb.RegisterAccessService(s, storageService)
	}

	if cfg.Storage.HasRole("deriver") {
		if err := registerViewRole(s, storageService, accessReader); err != nil {
			log.Errorf("初始化 ViewService 失败: %v", err)
			os.Exit(1)
		}
		log.Infof("ViewService role initialized")
	}

	if cfg.Storage.HasRole("deriver") {
		deriverService, err := startDeriverService(trpc.BackgroundContext(), cfg.Storage, opts, storageService, accessReader)
		if err != nil {
			log.Errorf("启动 deriver service 失败: %v", err)
			os.Exit(1)
		}
		log.Infof("Deriver role initialized")
		defer func() {
			if err := deriverService.Close(); err != nil {
				log.Errorf("关闭 deriver service 失败: %v", err)
			}
		}()
	}

	if cfg.Storage.HasRole("access") {
		if err := storageService.InitArchiveService(); err != nil {
			log.Errorf("初始化 ArchiveService 失败: %v", err)
			os.Exit(1)
		}
		timer.RegisterScheduler("archiveSchedule", &timer.DefaultScheduler{})
		registerTimerHandlerService("trpc.storage.archive.timer", s.Service("trpc.storage.archive.timer"), archive.HandleSchedule)
	}

	if shouldCreatePrimaryService(cfg.Storage) {
		primaryService := primarysvc.NewService(primarysvc.Options{
			Root:       opts.Root,
			PebblePath: opts.PebblePath,
		})
		defer func() {
			if err := primaryService.Close(); err != nil {
				log.Errorf("关闭 primary service 失败: %v", err)
			}
		}()
		pb.RegisterPrimaryStoreService(s, primaryService)
	}
	// 启动trpc服务器
	log.Infof("Storage roles %v serving", cfg.Storage.Roles)
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
	log.Warnf("Storage roles %v stopped", cfg.Storage.Roles)
}

func registerViewRole(s *server.Server, storageService *storagesvc.Service, accessReader deriver.AccessReader) error {
	if storageService == nil {
		return errors.New("view role requires storage service")
	}
	if err := storageService.InitViewBuilderWithFacts(accessReader); err != nil {
		return err
	}
	pb.RegisterDataViewService(s, storageService)
	timer.RegisterScheduler("viewBuilderSchedule", &timer.DefaultScheduler{})
	registerTimerHandlerService("trpc.storage.view.timer", s.Service("trpc.storage.view.timer"), view.HandleSchedule)
	registerTimerHandlerService("trpc.storage.view.cleanup.timer", s.Service("trpc.storage.view.cleanup.timer"), view.HandleSchedule)
	registerTimerHandlerService("trpc.storage.view.retry_failed.timer", s.Service("trpc.storage.view.retry_failed.timer"), view.HandleSchedule)
	return nil
}

func startDeriverService(ctx context.Context, storage storageconfig.StorageConfig, opts storagesvc.Options, storageService *storagesvc.Service, accessReader deriver.AccessReader) (*deriver.Service, error) {
	if storageService == nil {
		return nil, errors.New("deriver role requires storage service")
	}
	views, err := storageService.ViewStore()
	if err != nil {
		return nil, err
	}
	if accessReader == nil {
		accessReader = accessReaderForRuntime(storage, storageService)
	}
	service := deriver.NewService(deriver.Options{
		Events:         opts.Events,
		Reader:         accessReader,
		Metadata:       storageService.MetadataStore(),
		MetadataReader: storageService.MetadataReader(),
		Views:          views,
		Search:         storageService.SearchService(),
		BatchSize:      storage.Deriver.BatchSize,
		BatchWait:      time.Duration(storage.Deriver.BatchWaitMS) * time.Millisecond,
		MaxWorkers:     storage.Deriver.MaxWorkers,
	})
	if err := service.Start(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func accessReaderForRuntime(storage storageconfig.StorageConfig, storageService *storagesvc.Service) deriver.AccessReader {
	var local deriver.AccessReader
	accessServiceName := storage.Deriver.AccessServiceName
	if storage.HasRole("access") || shouldUseLocalAccessReader(storage) {
		local = storageService
		accessServiceName = ""
	}
	return deriver.NewAccessReader(local, accessServiceName)
}

func registerTimerHandlerService(name string, service server.Service, handle func(context.Context, string) error) bool {
	if service == nil {
		log.Warnf("timer service %s is not configured, skip register", name)
		return false
	}
	timer.RegisterHandlerService(service, handle)
	return true
}

func storageOptions() storagesvc.Options {
	configPath := configPathFromArgs(os.Args)
	storageConfigPath := storageConfigPathFromArgs(os.Args, configPath)
	cfg := loadRuntimeConfig(storageConfigPath)
	opts := storageOptionsFromConfig(cfg.Storage)
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		opts.Root = root
	}
	return opts
}

func needsRowsChangedBus(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("access") || storage.HasRole("deriver")
}

func shouldCreateStorageService(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("access") || storage.HasRole("deriver")
}

func shouldCreatePrimaryService(storage storageconfig.StorageConfig) bool {
	if storage.HasRole("primary") {
		return true
	}
	return storage.HasRole("access") && strings.TrimSpace(storage.Primary.ServiceName) == ""
}

func shouldUseLocalAccessReader(storage storageconfig.StorageConfig) bool {
	return storage.HasRole("access") && storage.HasRole("deriver") && isMemoryRowsChangedBus(storage.EventBus)
}

func isMemoryRowsChangedBus(cfg storageconfig.StorageEventBus) bool {
	kind := strings.ToLower(strings.TrimSpace(cfg.Type))
	return kind == "" || kind == "memory"
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

func storageConfigPathFromArgs(args []string, frameworkConfigPath string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-storage-conf=") {
			return strings.TrimPrefix(arg, "-storage-conf=")
		}
		if strings.HasPrefix(arg, "--storage-conf=") {
			return strings.TrimPrefix(arg, "--storage-conf=")
		}
		if (arg == "-storage-conf" || arg == "--storage-conf") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("MOOX_STORAGE_CONFIG"); path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_APP_CONFIG"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "storage.yaml")
	}
	if frameworkConfigPath != "" {
		return filepath.Join(filepath.Dir(frameworkConfigPath), "storage.yaml")
	}
	return filepath.Join("config", "storage.yaml")
}

func metadataInitRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-init-metadata" || arg == "--init-metadata" {
			return true
		}
	}
	return false
}

func initMetadataSchema(ctx context.Context, frameworkConfigPath string, storageConfigPath string) error {
	var storage storageconfig.StorageConfig
	if cfg, ok := loadStorageConfig(storageConfigPath); ok {
		storage = cfg.Storage
	}
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		storage.Root = root
	}
	return metadata.InitSchema(ctx, metadata.SchemaOptions{
		Storage:    storage,
		SchemaPath: metadataSchemaPath(frameworkConfigPath),
	})
}

func metadataImportRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-import-metadata" || arg == "--import-metadata" {
			return true
		}
	}
	return false
}

func importMetadataSeed(ctx context.Context, frameworkConfigPath string, storageConfigPath string) error {
	var storage storageconfig.StorageConfig
	if cfg, ok := loadStorageConfig(storageConfigPath); ok {
		storage = cfg.Storage
	}
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		storage.Root = root
	}
	seedPath := seedPathFromArgs(os.Args, storageConfigPath)
	result, err := metadata.ImportSeed(ctx, metadata.SeedOptions{
		Storage:    storage,
		SchemaPath: metadataSchemaPath(frameworkConfigPath),
		SeedPath:   seedPath,
	})
	if err != nil {
		return err
	}
	log.Infof("metadata seed 导入完成 (%s): spaces=%d data_sources=%d subjects=%d subject_symbols=%d datasets=%d dataset_subjects=%d fields=%d factors=%d dataset_columns=%d views=%d view_columns=%d primary_store_nodes=%d devices=%d primary_store_routes=%d",
		seedPath, result.Spaces, result.DataSources, result.Subjects, result.SubjectSymbols, result.Datasets,
		result.DatasetSubjects, result.Fields, result.Factors, result.DatasetColumns, result.Views,
		result.ViewColumns, result.PrimaryStoreNodes, result.Devices, result.PrimaryStoreRoutes)
	return nil
}

func seedPathFromArgs(args []string, storageConfigPath string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-seed=") {
			return strings.TrimPrefix(arg, "-seed=")
		}
		if strings.HasPrefix(arg, "--seed=") {
			return strings.TrimPrefix(arg, "--seed=")
		}
		if (arg == "-seed" || arg == "--seed") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if path := os.Getenv("STORAGE_SEED_FILE"); path != "" {
		return path
	}
	if storageConfigPath != "" {
		return filepath.Join(filepath.Dir(storageConfigPath), "metadata.seed.yaml")
	}
	return filepath.Join("config", "metadata.seed.yaml")
}

func metadataSchemaPath(configPath string) string {
	if path := os.Getenv("STORAGE_SCHEMA_FILE"); path != "" {
		return path
	}
	candidates := []string{}
	if configPath != "" {
		candidates = append(candidates, filepath.Clean(filepath.Join(filepath.Dir(configPath), "..", "schema", "metadata.sql")))
	}
	candidates = append(candidates,
		filepath.Join("schema", "metadata.sql"),
		filepath.Join("modules", "storage", "schema", "metadata.sql"),
	)
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func loadStorageOptions(configPath string) storagesvc.Options {
	cfg, ok := loadStorageConfig(configPath)
	if !ok {
		return storagesvc.Options{}
	}
	return storageOptionsFromConfig(cfg.Storage)
}

func storageOptionsFromConfig(storage storageconfig.StorageConfig) storagesvc.Options {
	return storagesvc.Options{
		Root:               storage.Root,
		MetadataPath:       storage.Metadata.Path,
		PebblePath:         storage.Devices.PebblePath,
		DuckDBPath:         storage.Devices.DuckDBPath,
		BlevePath:          storage.Devices.BlevePath,
		ParquetPath:        storage.Devices.ParquetPath,
		PrimaryServiceName: storage.Primary.ServiceName,
	}
}

func loadRuntimeConfig(configPath string) storageconfig.RuntimeConfig {
	if cfg, ok := loadStorageConfig(configPath); ok {
		return cfg
	}
	var cfg storageconfig.RuntimeConfig
	cfg.ApplyDefaults()
	return cfg
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
