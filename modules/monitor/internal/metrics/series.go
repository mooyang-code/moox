package metrics

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"sort"
)

// CanonicalLabelsJSON returns a stable JSON representation of labels.
func CanonicalLabelsJSON(labels map[string]string) (string, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	// encoding/json sorts string map keys, which gives us a canonical form.
	b, err := json.Marshal(labels)
	return string(b), err
}

// SeriesID hashes length-prefixed identity components. Length prefixes avoid
// collisions caused by ambiguous delimiters in user-controlled names/labels.
func SeriesID(serviceName, instanceID, metricName string, labels map[string]string) string {
	h := sha256.New()
	write := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	write(serviceName)
	write(instanceID)
	write(metricName)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		write(key)
		write(labels[key])
	}
	return fmtHex(h.Sum(nil))
}

func fmtHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2], out[i*2+1] = hex[v>>4], hex[v&15]
	}
	return string(out)
}
