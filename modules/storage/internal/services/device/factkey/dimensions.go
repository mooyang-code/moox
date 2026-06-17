package factkey

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
)

const emptyDimensionsHash = "_"

func DimensionsHash(values map[string]string) string {
	if len(values) == 0 {
		return emptyDimensionsHash
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		writePart(h, key)
		writePart(h, values[key])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writePart(h interface{ Write([]byte) (int, error) }, value string) {
	_, _ = h.Write([]byte(strconv.Itoa(len(value))))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte(";"))
}
