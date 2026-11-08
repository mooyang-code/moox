package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const archiveObjectStoreSection = `
[archive_object_store]
endpoint = "cos.ap-nanjing.myqcloud.com"
region = "ap-nanjing"
bucket = "moox-archive-1250000000"
prefix = "moox-archive/"
url_style = "virtual"
use_tls = true
storage_class = "STANDARD_IA"
secret_id = "secret_s3_writer"
`

func TestArchiveObjectStoreIsOptionalAndValidated(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.Nil(t, snapshot.Manifest.ArchiveObjectStore, "省略整段表示不使用对象存储")

	snapshot, err = Load(writeManifest(t, root, validManifest+archiveObjectStoreSection, 0o600), root)
	require.NoError(t, err)
	store := snapshot.Manifest.ArchiveObjectStore
	require.NotNil(t, store)
	assert.Equal(t, "cos.ap-nanjing.myqcloud.com", store.Endpoint)
	assert.Equal(t, "STANDARD_IA", store.StorageClass)
	assert.True(t, store.TLS())
	assert.Equal(t, "secret_s3_writer", store.SecretID)

	for name, tc := range map[string]struct{ old, new, want string }{
		"endpoint 带协议":  {`endpoint = "cos.ap-nanjing.myqcloud.com"`, `endpoint = "https://cos.ap-nanjing.myqcloud.com"`, "endpoint"},
		"region 缺失":     {`region = "ap-nanjing"`, ``, "region"},
		"bucket 缺失":     {`bucket = "moox-archive-1250000000"`, ``, "bucket"},
		"url_style 非法":  {`url_style = "virtual"`, `url_style = "auto"`, "url_style"},
		"secret_id 缺失":  {`secret_id = "secret_s3_writer"`, ``, "secret_id"},
		"prefix 不以斜杠结尾": {`prefix = "moox-archive/"`, `prefix = "moox-archive"`, "prefix"},
		"未知键":           {`use_tls = true`, "use_tls = true\npart_size = 1", "part_size"},
	} {
		body := validManifest + strings.Replace(archiveObjectStoreSection, tc.old, tc.new, 1)
		_, err := Load(writeManifest(t, root, body, 0o600), root)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}
