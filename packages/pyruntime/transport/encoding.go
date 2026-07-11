package transport

import (
	"encoding/json"
	"fmt"
)

type Encoding string

const (
	JSON        Encoding = "json"
	ArrowStream Encoding = "arrow_stream"
	ArrowMMap   Encoding = "arrow_mmap"
)

type Table struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

func EncodeJSON(t Table) ([]byte, error) { return json.Marshal(t) }
func DecodeJSON(b []byte) (Table, error) {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return Table{}, fmt.Errorf("decode table: %w", err)
	}
	return t, nil
}
