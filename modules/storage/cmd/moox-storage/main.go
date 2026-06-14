package main

import (
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mooyang-code/go-commlib/trpc-filter/cors"
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
	storageService := storagesvc.NewService(os.Getenv("MOOX_STORAGE_HOME"))
	pb.RegisterMetadataServiceService(s, storageService)
	pb.RegisterDataServiceService(s, storageService)
	pb.RegisterQueryServiceService(s, storageService)
	pb.RegisterAdapterServiceService(s, storageService)

	// 启动trpc服务器
	if err := s.Serve(); err != nil {
		log.Errorf("trpc服务器出错: %v", err)
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
