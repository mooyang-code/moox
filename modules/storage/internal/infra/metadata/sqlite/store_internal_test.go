package sqlite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithPragmasAddsConnectionPragmas(t *testing.T) {
	dsn := withPragmas("metadata.db")

	require.Contains(t, dsn, "_pragma=busy_timeout(5000)")
	require.Contains(t, dsn, "_pragma=journal_mode(WAL)")
	require.Contains(t, dsn, "_pragma=foreign_keys(ON)")
	require.True(t, strings.HasPrefix(dsn, "metadata.db?"))
}

func TestWithPragmasPreservesExistingQuery(t *testing.T) {
	dsn := withPragmas("metadata.db?cache=shared")

	require.Contains(t, dsn, "metadata.db?cache=shared&")
	require.Contains(t, dsn, "_pragma=foreign_keys(ON)")
}
