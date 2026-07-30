package duckdb

import "errors"

// ErrUnavailable identifies builds where the DuckDB engine cannot be used.
var ErrUnavailable = errors.New("duckdb view indexes require CGO")

func IsUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable)
}
