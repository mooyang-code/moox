package pebble

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	cpebble "github.com/cockroachdb/pebble"
)

func bindSourceStore(db *cpebble.DB, node string) (string, error) {
	key := []byte("__meta/source_store")
	var identity struct {
		Node string
		ID   string
	}
	data, closer, err := db.Get(key)
	if err == nil {
		defer closer.Close()
		if err := json.Unmarshal(data, &identity); err != nil {
			return "", err
		}
		if identity.Node != node || identity.ID == "" {
			return "", fmt.Errorf("DataNode store identity does not match configured node")
		}
		return identity.ID, nil
	}
	if !errors.Is(err, cpebble.ErrNotFound) {
		return "", err
	}
	iter, err := db.NewIter(nil)
	if err != nil {
		return "", err
	}
	hasData := iter.First()
	iterErr := errors.Join(iter.Error(), iter.Close())
	if iterErr != nil {
		return "", iterErr
	}
	if hasData {
		// Stores created before source-position incarnation metadata was
		// introduced have valid rows but no identity key. Assigning a fresh
		// identity is safe: there is no prior identity to preserve, and future
		// events will now carry a stable incarnation. Do not delete or rewrite
		// the existing data during this migration.
		identity.Node, identity.ID = node, rand.Text()
		data, err = json.Marshal(identity)
		if err != nil {
			return "", err
		}
		if err := db.Set(key, data, cpebble.Sync); err != nil {
			return "", err
		}
		return identity.ID, nil
	}
	identity.Node, identity.ID = node, rand.Text()
	data, err = json.Marshal(identity)
	if err != nil {
		return "", err
	}
	if err := db.Set(key, data, cpebble.Sync); err != nil {
		return "", err
	}
	return identity.ID, nil
}
