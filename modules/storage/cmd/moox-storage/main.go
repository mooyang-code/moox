package main

import (
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/services/common/config"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/services/storage"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	// 清除unix域套接字文件，避免内部使用unix域套接字的服务启动失败
	clearSocketFiles()

	// 创建trpc服务器
	s := trpc.NewServer()

	// 量化金融数据协议服务。当前实现提供真实的文件型读写路径，用于承接
	// Space/Subject/DataSet/View 等新概念和 CSV 验收数据。
	storageService := storagesvc.NewService(storageRoot())
	pb.RegisterMetadataServiceService(s, storageService)
	pb.RegisterDataServiceService(s, storageService)
	pb.RegisterQueryServiceService(s, storageService)
	pb.RegisterAdapterServiceService(s, storageService)

	// 启动trpc服务器
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
	}
}

func storageRoot() string {
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		return root
	}
	return loadStorageRoot(configPathFromArgs(os.Args))
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

func loadStorageRoot(configPath string) string {
	if configPath == "" {
		return ""
	}
	dir := filepath.Dir(configPath)
	file := filepath.Base(configPath)
	var cfg storageconfig.RuntimeConfig
	if err := storageconfig.NewConfigLoader(dir).LoadConfigWithDefaults(file, &cfg, cfg.ApplyDefaults); err != nil {
		log.Warnf("加载 storage 配置失败，使用默认目录: %v", err)
		return ""
	}
	return cfg.Storage.Root
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
