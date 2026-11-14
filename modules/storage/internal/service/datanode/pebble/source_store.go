package pebble

import (
	"crypto/rand"
	"encoding/json"
	"errors"

	cpebble "github.com/cockroachdb/pebble"
)

// bindSourceStore 返回库的实例标识：第一次打开时随机分配并落盘，之后沿用。
// 变更日志游标带它，库被清空重建后标识随之改变，读取方据此认出断档。
func bindSourceStore(db *cpebble.DB) (string, error) {
	key := []byte("__meta/source_store")
	var identity struct{ ID string }
	data, closer, err := db.Get(key)
	if err == nil {
		defer closer.Close()
		if err := json.Unmarshal(data, &identity); err != nil {
			return "", err
		}
		if identity.ID == "" {
			return "", errors.New("pebble 库的实例标识为空")
		}
		return identity.ID, nil
	}
	if !errors.Is(err, cpebble.ErrNotFound) {
		return "", err
	}
	identity.ID = rand.Text()
	data, err = json.Marshal(identity)
	if err != nil {
		return "", err
	}
	if err := db.Set(key, data, cpebble.Sync); err != nil {
		return "", err
	}
	return identity.ID, nil
}
