package fileserver

import (
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/constants"
)

// Config 文件服务器配置
type Config struct {
	Address    string // 服务地址
	Port       string // 服务端口
	PackageDir string // 包文件目录
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	Address:    "0.0.0.0",
	Port:       "18080",
	PackageDir: constants.GetPackageStorageDir(),
}
