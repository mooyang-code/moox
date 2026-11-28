package pebble

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/dataformat"
)

const (
	layoutMarkerName       = "storage_layout_version"
	layoutMarkerTempPrefix = "." + layoutMarkerName + ".tmp-"
)

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
		switch string(data) {
		case layoutVersion:
			return cleanupLayoutMarkerTemps(path)
		default:
			return validateLayoutMarker(data)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("读取 Pebble 库的布局版本文件：%w", err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("检查 Pebble 库目录：%w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), layoutMarkerTempPrefix) {
			if markerData, markerErr := os.ReadFile(markerPath); markerErr == nil {
				if err := validateLayoutMarker(markerData); err != nil {
					return err
				}
				return cleanupLayoutMarkerTemps(path)
			}
			return errors.New("Pebble 库目录里有数据却没有布局版本文件；需要清空 Pebble 库后重建")
		}
	}
	if err := createLayoutMarker(path, markerPath); err != nil {
		return fmt.Errorf("创建 Pebble 库的布局版本文件：%w", err)
	}
	return cleanupLayoutMarkerTemps(path)
}

func validateLayoutMarker(data []byte) error {
	if string(data) != layoutVersion {
		return fmt.Errorf("Pebble 库的布局版本 %q 不受支持或已损坏；需要清空 Pebble 库后重建", string(data))
	}
	return nil
}

func cleanupLayoutMarkerTemps(dirPath string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("检查 Pebble 库目录：%w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), layoutMarkerTempPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dirPath, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除残留的布局版本临时文件 %q：%w", entry.Name(), err)
		}
	}
	return nil
}

func createLayoutMarker(dirPath, markerPath string) error {
	temp, err := os.CreateTemp(dirPath, layoutMarkerTempPrefix)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if _, err := io.WriteString(temp, layoutVersion); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, markerPath); err != nil {
		data, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			return err
		}
		if markerErr := validateLayoutMarker(data); markerErr != nil {
			return markerErr
		}
	}
	_ = os.Remove(tempPath)
	return syncDirectory(dirPath)
}

func syncDirectory(dirPath string) error {
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
