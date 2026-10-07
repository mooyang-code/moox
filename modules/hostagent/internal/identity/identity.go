package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"gopkg.in/yaml.v3"
)

type File struct {
	Version   int       `yaml:"version"`
	AgentID   string    `yaml:"agent_id"`
	CreatedAt time.Time `yaml:"created_at"`
}

const fileVersion = 3

// LoadOrCreate returns the host's persistent compact identity, creating it on
// first start.
func LoadOrCreate(path string) (File, error) {
	if path == "" {
		return File{}, fmt.Errorf("identity path is empty")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		agentID, err := hostmetricpb.NewAgentID()
		if err != nil {
			return File{}, err
		}
		f := File{Version: fileVersion, AgentID: agentID, CreatedAt: time.Now().UTC()}
		if err := persist(path, f); err != nil {
			return File{}, err
		}
		return f, nil
	}
	if err != nil {
		return File{}, err
	}
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
	if err := yaml.Unmarshal(raw, &f); err != nil || f.Version != fileVersion || !hostmetricpb.IsAgentID(f.AgentID) {
		return File{}, fmt.Errorf("invalid identity file")
	}
	return f, nil
}

func persist(path string, f File) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".identity-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	raw, err := yaml.Marshal(f)
	if err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
