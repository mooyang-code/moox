package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSubject(t *testing.T) {
	require.NoError(t, validateSubject("moox.storage.>", true))
	require.Error(t, validateSubject("", true))
	require.Error(t, validateSubject("moox..storage", true))
	require.Error(t, validateSubject("moox.>", false))
	require.Error(t, validateSubject("moox.*.rows", false))
}

func TestTopicVersion(t *testing.T) {
	version, err := topicVersion("moox.storage.rows_updated.v1")
	require.NoError(t, err)
	assert.Equal(t, uint32(1), version)
	_, err = topicVersion("moox.storage.rows_updated")
	require.Error(t, err)
}

func TestSubjectMatches(t *testing.T) {
	assert.True(t, subjectMatches("moox.storage.>", "moox.storage.rows.v1"))
	assert.True(t, subjectMatches("moox.*.rows", "moox.storage.rows"))
	assert.False(t, subjectMatches("moox.storage.rows", "moox.storage.other"))
}

func TestPatternsOverlap(t *testing.T) {
	assert.True(t, patternsOverlap("moox.>", "moox.storage"))
	assert.True(t, patternsOverlap("moox.*.rows", "moox.storage.rows"))
	assert.False(t, patternsOverlap("moox.storage", "moox.factor"))
}

func TestUnsafeStoreDir(t *testing.T) {
	assert.True(t, unsafeStoreDir(""))
	assert.True(t, unsafeStoreDir("."))
	assert.True(t, unsafeStoreDir("/"))
	assert.False(t, unsafeStoreDir("./data/eventbus"))
}

func TestValidCloudNodeFamily(t *testing.T) {
	assert.True(t, validCloudNodeFamily("moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*"))
	assert.False(t, validCloudNodeFamily("moox.cloudnode.exec.v1"))
}

func TestFindStream(t *testing.T) {
	cfg := Default()
	stream, ok := findStream(cfg, cfg.Streams[0].Name)
	require.True(t, ok)
	assert.Equal(t, cfg.Streams[0].Name, stream.Name)
	_, ok = findStream(cfg, "missing")
	assert.False(t, ok)
}

func TestValidateConsumerTemplate(t *testing.T) {
	cfg := Default()
	template := cfg.ConsumerTemplates[0]
	require.NoError(t, validateConsumerTemplate(&template, cfg))
	bad := template
	bad.Stream = "missing"
	require.Error(t, validateConsumerTemplate(&bad, cfg))
}
