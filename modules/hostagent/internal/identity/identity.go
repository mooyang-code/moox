package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"gopkg.in/yaml.v3"
)

type File struct {
	Version   int       `yaml:"version"`
	AgentID   string    `yaml:"agent_id"`
	CompactID string    `yaml:"compact_agent_id,omitempty"`
	LegacyID  string    `yaml:"legacy_agent_id,omitempty"`
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
		if err := yaml.Unmarshal(raw, &f); err != nil || !hostmetricpb.IsCompatibleAgentID(f.AgentID) {
			return File{}, fmt.Errorf("invalid identity file")
		}
		if hostmetricpb.IsAgentID(f.AgentID) {
			// During the compatibility window, keep the legacy value in the
			// agent_id field on disk so an older HostAgent can be rolled back.
			// A previous development build may already have written compact ID
			// plus legacy_agent_id; normalize that shape back to the compatible
			// representation before returning the compact runtime identity.
			if hostmetricpb.IsLegacyAgentID(f.LegacyID) {
				compact, mapErr := hostmetricpb.CompactAgentIDForLegacy(f.LegacyID)
				if mapErr != nil {
					return File{}, mapErr
				}
				stored := f
				stored.Version = 2
				stored.AgentID = f.LegacyID
				stored.CompactID = compact
				if err := persist(path, stored); err != nil {
					return File{}, fmt.Errorf("normalize identity file: %w", err)
				}
				f.AgentID = compact
				f.CompactID = compact
				f.Version = 2
				return f, nil
			}
			return f, nil
		}
		// Older releases persisted UUIDs. Rotate them once to the compact ID
		// while retaining the old value for Monitor's storage alias migration.
		legacyID := f.AgentID
		compact, err := hostmetricpb.CompactAgentIDForLegacy(legacyID)
		if err != nil {
			return File{}, err
		}
		stored := f
		stored.AgentID = legacyID
		stored.CompactID = compact
		stored.LegacyID = legacyID
		stored.Version = 2
		if stored.CreatedAt.IsZero() {
			stored.CreatedAt = time.Now().UTC()
		}
		if err := persist(path, stored); err != nil {
			return File{}, fmt.Errorf("migrate identity file: %w", err)
		}
		f.AgentID = compact
		f.CompactID = compact
		f.LegacyID = legacyID
		f.Version = 2
		f.CreatedAt = stored.CreatedAt
		return f, nil
	} else if !os.IsNotExist(err) {
		return File{}, err
	}
	legacyID := uuid.New().String()
	agentID, err := hostmetricpb.CompactAgentIDForLegacy(legacyID)
	if err != nil {
		return File{}, err
	}
	now := time.Now().UTC()
	stored := File{Version: 2, AgentID: legacyID, CompactID: agentID, LegacyID: legacyID, CreatedAt: now}
	if err := persist(path, stored); err != nil {
		return File{}, err
	}
	return File{Version: 2, AgentID: agentID, CompactID: agentID, LegacyID: legacyID, CreatedAt: now}, nil
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
