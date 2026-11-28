package pebble

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mooyang-code/moox/packages/dataformat"
)

const layoutMarkerName = "storage_layout_version"

// 版本号与 packages/dataformat.StoragePebbleLayoutVersion 保持单一来源：
// 版本 5 在版本 4（字段、属性、按时间与按标的排序的两种历史行索引）之上，周期账本改为每期一条记录（__period/），
// 完成标记按“数据集/频率/周期/来源/触发事件”存放（__marker/）。其他版本的数据都要显式重置后再用，不做原地升级。
var layoutVersion = strconv.Itoa(dataformat.StoragePebbleLayoutVersion) + "\n"

// IsCompatibleOrEmpty 报告 Pebble 目录尚不存在/为空，或已存在且布局版本与当前版本一致。
func IsCompatibleOrEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return true, nil
	}
	data, err := os.ReadFile(filepath.Join(path, layoutMarkerName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return string(data) == layoutVersion, nil
}

func ensureLayout(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("创建 Pebble 库目录：%w", err)
	}

	markerPath := filepath.Join(path, layoutMarkerName)
	data, err := os.ReadFile(markerPath)
	switch {
	case err == nil:
		return validateLayoutMarker(data)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("读取 Pebble 库的布局版本文件：%w", err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("检查 Pebble 库目录：%w", err)
	}
	if len(entries) > 0 {
		return errors.New("Pebble 库目录里有数据却没有布局版本文件；需要清空 Pebble 库后重建")
	}
	if err := os.WriteFile(markerPath, []byte(layoutVersion), 0o644); err != nil {
		return fmt.Errorf("创建 Pebble 库的布局版本文件：%w", err)
	}
	return nil
}

func validateLayoutMarker(data []byte) error {
	if string(data) != layoutVersion {
		return fmt.Errorf("Pebble 库的布局版本 %q 不受支持或已损坏；需要清空 Pebble 库后重建", string(data))
	}
	return nil
}
