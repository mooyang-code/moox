//go:build cgo

package duckdb

import (
	"testing"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestQuoteColumnNameInvalidReturnsError(t *testing.T) {
	_, err := quoteColumnName(`bad"column`)
	if err == nil {
		t.Fatal("expected error for invalid column name")
	}
}

func TestQuoteIndexNameInvalidReturnsError(t *testing.T) {
	_, err := quoteIndexName("bad-name!", "suffix")
	if err == nil {
		t.Fatal("expected error for invalid index name")
	}
}

func TestBuildFilterPredicatesInvalidColumnReturnsError(t *testing.T) {
	_, _, err := buildFilterPredicates([]*pb.FilterExpr{
		{Expr: `bad"col == 'x'`},
	}, nil)
	if err == nil {
		t.Fatal("expected error for invalid filter column")
	}
}
