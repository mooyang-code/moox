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
	// 还没有身份：分配新的身份。之后的事件都带这个稳定的实例标识。
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
