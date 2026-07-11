package readiness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/builtin"
	"github.com/mooyang-code/moox/packages/marketmanifest"
)

type Lock struct {
	Version       int               `json:"version"`
	Markets       map[string]Market `json:"markets"`
	FactorySHA256 string            `json:"factory_sha256"`
}
type Market struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	RuntimeEnabled bool   `json:"runtime_enabled"`
}

func Generate(root string) (Lock, error) {
	manifests, err := marketmanifest.LoadDir(root)
	if err != nil {
		return Lock{}, err
	}
	catalog := builtin.Default(filepath.Join(root, "stock_cn", "calendar.yaml"))
	factoryIdentity := strings.Join(catalog.MarketIDs(), ",") + "|" + strings.Join(catalog.ProviderIDs(), ",")
	lock := Lock{Version: 1, Markets: make(map[string]Market, len(manifests)), FactorySHA256: digest([]byte(factoryIdentity))}
	for _, manifest := range manifests {
		dir := filepath.Join(root, manifest.MarketID)
		manifestBytes, err := os.ReadFile(filepath.Join(dir, "market.yaml"))
		if err != nil {
			return Lock{}, err
		}
		evidencePath := filepath.Join(dir, "provider-validation.yaml")
		evidence, err := marketmanifest.LoadValidationFile(evidencePath)
		if err != nil {
			return Lock{}, err
		}
		if manifest.RuntimeEnabled {
			validUntil, parseErr := time.Parse(time.RFC3339, evidence.ValidUntil)
			if parseErr != nil || !validUntil.After(time.Now().UTC()) || !evidence.CapabilityEnabled || !evidence.Network.Reachable {
				return Lock{}, fmt.Errorf("runtime-enabled market %s lacks current enabled reachable provider evidence", manifest.MarketID)
			}
		}
		evidenceBytes, err := os.ReadFile(evidencePath)
		if err != nil {
			return Lock{}, err
		}
		lock.Markets[manifest.MarketID] = Market{ManifestSHA256: digest(manifestBytes), EvidenceSHA256: digest(evidenceBytes), RuntimeEnabled: manifest.RuntimeEnabled}
	}
	return lock, nil
}
func (l Lock) Validate(root string) error {
	expected, err := Generate(root)
	if err != nil {
		return err
	}
	if l.Version != expected.Version {
		return fmt.Errorf("readiness lock version mismatch")
	}
	if l.FactorySHA256 != expected.FactorySHA256 {
		return fmt.Errorf("readiness lock factory catalog mismatch")
	}
	if len(l.Markets) != len(expected.Markets) {
		return fmt.Errorf("readiness lock market count mismatch")
	}
	for id, want := range expected.Markets {
		if got, ok := l.Markets[id]; !ok || got != want {
			return fmt.Errorf("readiness lock mismatch for %s", id)
		}
	}
	return nil
}
func Write(path string, lock Lock) error {
	raw, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
func Read(path string) (Lock, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, err
	}
	var lock Lock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return Lock{}, err
	}
	return lock, nil
}
func ValidateRuntime(path, spaceID string) error {
	lock, err := Read(path)
	if err != nil {
		return fmt.Errorf("read market readiness lock: %w", err)
	}
	market, ok := lock.Markets[spaceID]
	if !ok {
		return fmt.Errorf("space %q is not present in market readiness lock", spaceID)
	}
	if !market.RuntimeEnabled {
		return fmt.Errorf("space %q is not runtime enabled", spaceID)
	}
	return nil
}
func MarketIDs(lock Lock) []string {
	ids := make([]string, 0, len(lock.Markets))
	for id := range lock.Markets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
