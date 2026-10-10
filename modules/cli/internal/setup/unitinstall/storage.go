package unitinstall

import (
	"errors"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func storageComponent(id string) bool {
	return slices.Contains([]string{"storage-primary", "storage-node", "storage-view"}, id)
}

func configureStorageEnvironment(id string, values map[string]string) error {
	if !storageComponent(id) {
		return nil
	}
	if values == nil {
		return errors.New("Storage requires an explicit private runtime environment")
	}
	for key, expected := range map[string]string{
		"MOOX_STORAGE_ROLE":          strings.TrimPrefix(id, "storage-"),
		"MOOX_STORAGE_HOME":          "./var/storage",
		"MOOX_STORAGE_CONFIG":        "config/storage.yaml",
		"MOOX_STORAGE_METADATA_PATH": "./var/storage/metadata/storage_metadata.db",
	} {
		if actual, exists := values[key]; exists && actual != expected {
			return errors.New("Storage runtime paths and role must match the managed candidate layout")
		}
		values[key] = expected
	}
	return nil
}

func renderStorageConfiguration(document *yaml.Node) error {
	storage := member(document, "storage")
	if storage == nil {
		return nil
	}
	// Legacy templates share ../config; each native component instead owns
	// its policy and all writable paths inside its independent release tree.
	for _, field := range []struct {
		key   string
		value any
	}{
		{"root", "./var/storage"},
		{"policy_file", "config/storage-policy.json"},
		{"metadata", map[string]string{"path": "./var/storage/metadata/storage_metadata.db"}},
		{"devices", map[string]string{"pebble_path": "./var/storage/pebble", "view_index_root": "./var/storage/view-indexes"}},
	} {
		if err := setValue(storage, field.key, field.value); err != nil {
			return err
		}
	}
	return nil
}
