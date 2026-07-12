package cosstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeObjectClient struct {
	puts []string
	err  error
}

func (f *fakeObjectClient) Put(_ context.Context, key, localPath string) error {
	f.puts = append(f.puts, key+":"+localPath)
	return f.err
}

func TestSyncUploadsParquetFiles(t *testing.T) {
	root := t.TempDir()
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	path := filepath.Join(root, "crypto", "kline", pastMonth+".parquet")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("parquet"), 0o600))
	client := &fakeObjectClient{}
	err := Syncer{Client: client, Root: root, Prefix: "moox/archive", Workers: 1}.Sync(context.Background())
	require.NoError(t, err)
	require.Len(t, client.puts, 1)
	assert.Contains(t, client.puts[0], "moox/archive/")
}

func TestSyncRejectsNilClient(t *testing.T) {
	err := (Syncer{}).Sync(context.Background())
	require.Error(t, err)
}

func TestSyncPropagatesPutError(t *testing.T) {
	root := t.TempDir()
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	path := filepath.Join(root, pastMonth+".parquet")
	require.NoError(t, os.WriteFile(path, []byte("parquet"), 0o600))
	client := &fakeObjectClient{err: assert.AnError}
	err := Syncer{Client: client, Root: root, Workers: 1}.Sync(context.Background())
	require.Error(t, err)
}

func TestIsCurrentMonth(t *testing.T) {
	current := time.Now().UTC().Format("200601") + ".parquet"
	assert.True(t, isCurrentMonth(current))
	assert.False(t, isCurrentMonth("199901.parquet"))
	assert.False(t, isCurrentMonth("short"))
}

func TestSyncSkipsCurrentMonthWhenDisabled(t *testing.T) {
	root := t.TempDir()
	current := time.Now().UTC().Format("200601") + ".parquet"
	require.NoError(t, os.WriteFile(filepath.Join(root, current), []byte("parquet"), 0o600))
	client := &fakeObjectClient{}
	err := Syncer{Client: client, Root: root, SyncOpenPartitions: false}.Sync(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.puts)
}
