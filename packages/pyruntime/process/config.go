package process

import (
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"os"
	"time"
)

type Config struct {
	PythonBin  string
	WorkerPath string
	Args       []string
	// Env 是追加给 worker 子进程的环境变量（KEY=VALUE），叠加在继承的父进程环境之上，同名时以这里为准。
	Env         []string
	Limits      protocol.Limits
	Hello       protocol.HelloExpectation
	TaskTimeout time.Duration
	MaxLogBytes int
}

func (c *Config) defaults() {
	if c.PythonBin == "" {
		c.PythonBin = "python3"
	}
	if c.TaskTimeout <= 0 {
		c.TaskTimeout = 30 * time.Second
	}
	if c.MaxLogBytes <= 0 {
		c.MaxLogBytes = 64 << 10
	}
	if c.Limits.MaxFrameBytes == 0 {
		c.Limits = protocol.DefaultLimits()
	}
}

func DefaultLimits() protocol.Limits { return protocol.DefaultLimits() }

// singleThreadBLASKeys 是 numpy 依赖的 BLAS/OpenMP 库读取的线程池大小变量。
var singleThreadBLASKeys = []string{"OPENBLAS_NUM_THREADS", "OMP_NUM_THREADS", "MKL_NUM_THREADS"}

// SingleThreadBLASEnv 返回把 BLAS/OpenMP 线程池限制为 1 的环境变量，供 Config.Env 使用。
// numpy 默认按核数开线程池，常驻 worker 多时线程数成倍膨胀；只做逐元素与滚动运算的 worker 用不到这些线程。
// 父进程环境里已显式设置的变量不出现在结果里，子进程继承原值。
func SingleThreadBLASEnv() []string {
	env := make([]string, 0, len(singleThreadBLASKeys))
	for _, key := range singleThreadBLASKeys {
		if _, ok := os.LookupEnv(key); !ok {
			env = append(env, key+"=1")
		}
	}
	return env
}
