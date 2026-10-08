// Package store 持久化主机网关的快照缓存和 nonce。
package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"google.golang.org/protobuf/encoding/protojson"
)

const maxSnapshotFileBytes = 16 << 20

// Snapshots 把最近一次成功应用的快照落盘（snapshot.json，权限 0600）。网关控制不可用时，
// 网关重启后先加载缓存，继续用缓存的路由、目录和校验密钥服务。
type Snapshots struct{ directory string }

// NewSnapshots 创建快照缓存。
func NewSnapshots(directory string) *Snapshots { return &Snapshots{directory: directory} }

// Path 返回缓存文件路径。
func (s *Snapshots) Path() string { return filepath.Join(s.directory, "snapshot.json") }

// Check 检查缓存目录和文件的权限，供 /healthz 使用。
func (s *Snapshots) Check() error {
	if err := ensureSecureDirectory(s.directory, false); err != nil {
		return err
	}
	return ensureSecureFile(s.Path(), true)
}

// Save 原子写入快照。
func (s *Snapshots) Save(snapshot *adminpb.HostSnapshot) (resultErr error) {
	if snapshot == nil {
		return errors.New("快照为空")
	}
	if err := ensureSecureDirectory(s.directory, true); err != nil {
		return err
	}
	if err := ensureSecureFile(s.Path(), true); err != nil {
		return err
	}
	encoded, err := protojson.MarshalOptions{Multiline: true}.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("编码快照: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(s.directory, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("创建快照临时文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("设置快照临时文件权限: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("写入快照临时文件: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("同步快照临时文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭快照临时文件: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path()); err != nil {
		return fmt.Errorf("替换快照文件: %w", err)
	}
	directory, err := os.Open(s.directory)
	if err != nil {
		return fmt.Errorf("打开快照目录: %w", err)
	}
	defer directory.Close()
	return directory.Sync()
}

// Load 读取缓存的快照；没有缓存时返回 os.ErrNotExist。校验由调用方完成。
func (s *Snapshots) Load() (*adminpb.HostSnapshot, error) {
	if err := ensureSecureDirectory(s.directory, false); err != nil {
		return nil, err
	}
	if err := ensureSecureFile(s.Path(), false); err != nil {
		return nil, err
	}
	file, err := os.Open(s.Path())
	if err != nil {
		return nil, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxSnapshotFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取快照缓存: %w", err)
	}
	if len(encoded) > maxSnapshotFileBytes {
		return nil, errors.New("快照缓存过大")
	}
	snapshot := &adminpb.HostSnapshot{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(encoded, snapshot); err != nil {
		return nil, fmt.Errorf("解析快照缓存: %w", err)
	}
	return snapshot, nil
}

func ensureSecureDirectory(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("创建快照目录: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("快照目录必须是真实目录，不能是符号链接")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("快照目录只能由属主访问（当前 %04o）", info.Mode().Perm())
	}
	return nil
}

func ensureSecureFile(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("快照缓存必须是普通文件，不能是符号链接")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("快照缓存只能由属主读写（当前 %04o）", info.Mode().Perm())
	}
	return nil
}
