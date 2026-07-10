package identity

import (
	"fmt"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type File struct {
	Version   int       `yaml:"version"`
	AgentID   string    `yaml:"agent_id"`
	CreatedAt time.Time `yaml:"created_at"`
}

func LoadOrCreate(path string) (File, error) {
	if path == "" {
		return File{}, fmt.Errorf("identity path is empty")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return File{}, fmt.Errorf("identity file must be regular 0600")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
			return File{}, fmt.Errorf("identity owner mismatch")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return File{}, err
		}
		var f File
		if err := yaml.Unmarshal(raw, &f); err != nil || uuid.Validate(f.AgentID) != nil {
			return File{}, fmt.Errorf("invalid identity file")
		}
		return f, nil
	} else if !os.IsNotExist(err) {
		return File{}, err
	}
	f := File{Version: 1, AgentID: uuid.New().String(), CreatedAt: time.Now().UTC()}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return File{}, err
	}
	tmp, err := os.CreateTemp(dir, ".identity-*")
	if err != nil {
		return File{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return File{}, err
	}
	raw, _ := yaml.Marshal(f)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return File{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return File{}, err
	}
	if err := tmp.Close(); err != nil {
		return File{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return File{}, err
	}
	return f, nil
}
