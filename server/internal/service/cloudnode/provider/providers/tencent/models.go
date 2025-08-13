package tencent

// 腾讯云特定的模型定义

// TencentFunctionConfig 腾讯云函数配置
type TencentFunctionConfig struct {
	Handler    string // 入口函数
	Runtime    string // 运行时环境，如：Go1、Python3.7等
	MemorySize int64  // 内存大小，单位MB
	Timeout    int64  // 超时时间，单位秒
}

// 腾讯云运行时常量
const (
	RuntimeGo1       = "Go1"
	RuntimePython27  = "Python2.7"
	RuntimePython36  = "Python3.6"
	RuntimePython37  = "Python3.7"
	RuntimeNodejs89  = "Nodejs8.9"
	RuntimeNodejs124 = "Nodejs12.16"
	RuntimeNodejs146 = "Nodejs14.18"
	RuntimePHP72     = "PHP7.2"
	RuntimePHP74     = "PHP7.4"
	RuntimeJava8     = "Java8"
	RuntimeJava11    = "Java11"
)

// 默认配置
const (
	DefaultMemorySize = 128
	DefaultTimeout    = 3
	DefaultRegion     = "ap-guangzhou"
)
