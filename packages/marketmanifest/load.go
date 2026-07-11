package marketmanifest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func LoadFile(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	if err := rejectSecrets(data); err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest %s: %w", path, err)
	}
	if err := manifest.Validate(filepath.Base(filepath.Dir(path))); err != nil {
		return Manifest{}, fmt.Errorf("validate manifest %s: %w", path, err)
	}
	return manifest, nil
}

func LoadDir(dir string) ([]Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	manifests := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), "market.yaml")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		manifest, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func LoadValidationFile(path string) (ProviderValidation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProviderValidation{}, err
	}
	if err := rejectSecrets(data); err != nil {
		return ProviderValidation{}, err
	}
	var evidence ProviderValidation
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&evidence); err != nil {
		return ProviderValidation{}, fmt.Errorf("decode validation evidence: %w", err)
	}
	if evidence.SchemaVersion == 0 {
		evidence.SchemaVersion = 1
	}
	if evidence.ProviderID == "" {
		return ProviderValidation{}, fmt.Errorf("provider_id is required")
	}
	return evidence, nil
}
