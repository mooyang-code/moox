package unitruntime

import (
	"fmt"
	"os"
)

const (
	// 组件的标准输出/错误直接追加写入 run/logs/<组件>.log，没有常驻进程可以实时切割，所以在每次启动前按大小轮转，
	// 限制日志跨重启的累计大小；运行期间各模块的框架日志由它们自己的日志写入器（trpc 的 file writer）按大小轮转，
	// 标准输出/错误里剩下的崩溃信息和数据库日志持续报错时仍可能增长。
	runtimeLogMaxBytes = 50 << 20
	runtimeLogBackups  = 5
)

// rotateRuntimeLog 在日志超过 maxBytes 时把 name 轮转为 name.1，旧备份依次后移，最多保留 backups 份。
// 文件不存在或不超限时什么都不做。
func rotateRuntimeLog(root *os.Root, name string, maxBytes int64, backups int) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) || (err == nil && info.Size() <= maxBytes) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := root.Remove(fmt.Sprintf("%s.%d", name, backups)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := backups - 1; i >= 1; i-- {
		if err := root.Rename(fmt.Sprintf("%s.%d", name, i), fmt.Sprintf("%s.%d", name, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return root.Rename(name, name+".1")
}
