package configcrypt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// KeyStore 保存本机唯一的一把配置加密密钥。
type KeyStore interface {
	// Load 读出密钥；没有时返回 ErrKeyMissing。
	Load() ([]byte, error)
	// LoadOrCreate 读出密钥，没有就生成一把保存起来。
	LoadOrCreate() ([]byte, error)
	// Where 用中文说明密钥放在哪里，给页面和命令输出用。
	Where() string
}

const (
	keyringService = "MooX"
	keyringUser    = "moox.toml 加密密钥"
)

var (
	defaultOnce  sync.Once
	defaultStore KeyStore
)

// Default 返回本机的密钥存放位置：有系统钥匙串就用钥匙串，否则用操作员目录下仅本人可读的密钥文件。
// 结果在进程内缓存，避免反复探测钥匙串。
func Default() KeyStore {
	defaultOnce.Do(func() {
		if keychainUsable() {
			defaultStore = keychainStore{}
			return
		}
		defaultStore = fileStore{path: defaultKeyFile()}
	})
	return defaultStore
}

// SetDefaultForTest 在测试里替换密钥存放位置，返回恢复函数。
func SetDefaultForTest(store KeyStore) func() {
	defaultOnce.Do(func() {})
	previous := defaultStore
	defaultStore = store
	return func() { defaultStore = previous }
}

// keychainUsable 用一次读取探测钥匙串：读到或「没有这个条目」都说明钥匙串可用。
func keychainUsable() bool {
	_, err := keyring.Get(keyringService, keyringUser)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

type keychainStore struct{}

func (keychainStore) Load() ([]byte, error) {
	value, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrKeyMissing
	}
	if err != nil {
		return nil, fmt.Errorf("读取%s里的密钥失败：%w", keychainName(), err)
	}
	return decodeKey(value)
}

func (s keychainStore) LoadOrCreate() ([]byte, error) {
	key, err := s.Load()
	if !errors.Is(err, ErrKeyMissing) {
		return key, err
	}
	key, err = newKey()
	if err != nil {
		return nil, err
	}
	if err := keyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("把密钥存进%s失败：%w", keychainName(), err)
	}
	return key, nil
}

func (keychainStore) Where() string { return keychainName() }

func keychainName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS 钥匙串"
	case "windows":
		return "Windows 凭据管理器"
	default:
		return "系统钥匙串"
	}
}

type fileStore struct{ path string }

func defaultKeyFile() string {
	dir, err := os.UserConfigDir()
	if home, homeErr := os.UserHomeDir(); homeErr == nil && runtime.GOOS != "windows" {
		dir, err = filepath.Join(home, ".config"), nil
	}
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "moox", "config.key")
}

func (s fileStore) Load() ([]byte, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrKeyMissing
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nil, fmt.Errorf("密钥文件 %s 必须是普通文件且只有本人可读（0600）", s.path)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	return decodeKey(strings.TrimSpace(string(raw)))
}

func (s fileStore) LoadOrCreate() ([]byte, error) {
	key, err := s.Load()
	if !errors.Is(err, ErrKeyMissing) {
		return key, err
	}
	key, err = newKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建密钥文件 %s 失败：%w", s.path, err)
	}
	if _, err := file.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return key, file.Close()
}

func (s fileStore) Where() string { return "本机密钥文件（仅本人可读）" }

func newKey() ([]byte, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func decodeKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != keySize {
		return nil, errors.New("保存的加密密钥已损坏")
	}
	return key, nil
}

// MemoryStore 是测试用的内存密钥。
type MemoryStore struct {
	mu  sync.Mutex
	key []byte
}

func (m *MemoryStore) Load() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.key == nil {
		return nil, ErrKeyMissing
	}
	return append([]byte(nil), m.key...), nil
}

func (m *MemoryStore) LoadOrCreate() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.key == nil {
		key, err := newKey()
		if err != nil {
			return nil, err
		}
		m.key = key
	}
	return append([]byte(nil), m.key...), nil
}

func (m *MemoryStore) Where() string { return "测试内存" }
