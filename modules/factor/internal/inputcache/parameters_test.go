package inputcache

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/require"
)

type nullableUnsigned uint64

func (nullableUnsigned) Value() (driver.Value, error) { return nil, nil }

func TestCacheParameterPreservesValuerSemantics(t *testing.T) {
	value := nullableUnsigned(5)
	require.Equal(t, value, cacheParameter("UBIGINT", value))
	require.Equal(t, &value, cacheParameter("UBIGINT", &value))
	pointer := &value
	require.Equal(t, &pointer, cacheParameter("UBIGINT", &pointer))
}

func TestCacheParameterOnlyConvertsUnsignedColumnValues(t *testing.T) {
	max := ^uint64(0)
	require.Equal(t, "18446744073709551615", cacheParameter("UBIGINT", max))
	require.Equal(t, "18446744073709551615", cacheParameter("UBIGINT", &max))
	require.Equal(t, max, cacheParameter("VARCHAR", max))
	require.Nil(t, cacheParameter("UBIGINT", nil))
	require.Equal(t, (*uint64)(nil), cacheParameter("UBIGINT", (*uint64)(nil)))
}
