package viewindex

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// IndexRef 是从索引标识解析出的数据集身份：每个数据集恰好有一个查询索引。
type IndexRef struct {
	SpaceID   string
	DatasetID string
}

// IndexID 返回数据集唯一的物理索引标识。
func IndexID(spaceID, datasetID string) string {
	return fmt.Sprintf("idx_s%s_d%s", encode(spaceID), encode(datasetID))
}

// ParseIndexID 解析 IndexID 生成的标识。
func ParseIndexID(id string) (IndexRef, error) {
	var ref IndexRef
	parts := strings.Split(id, "_")
	if len(parts) != 3 || parts[0] != "idx" || !strings.HasPrefix(parts[1], "s") || !strings.HasPrefix(parts[2], "d") {
		return ref, errors.New("invalid view index id")
	}
	ref.SpaceID = decode(parts[1][1:])
	ref.DatasetID = decode(parts[2][1:])
	if ref.SpaceID == "" || ref.DatasetID == "" || IndexID(ref.SpaceID, ref.DatasetID) != id {
		return ref, errors.New("invalid view index id")
	}
	return ref, nil
}

func encode(v string) string { return hex.EncodeToString([]byte(v)) }
func decode(v string) string {
	raw, err := hex.DecodeString(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
