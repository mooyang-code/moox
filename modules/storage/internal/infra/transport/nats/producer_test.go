package nats

import (
	"errors"
	"testing"

	natslib "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestEnsureStreamUpdatesWhenStreamAlreadyExists(t *testing.T) {
	manager := &fakeStreamManager{addErr: natslib.ErrStreamNameAlreadyInUse}
	cfg := &natslib.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.storage.>"}}

	err := ensureStream(manager, cfg)

	require.NoError(t, err)
	require.Equal(t, 1, manager.adds)
	require.Equal(t, 1, manager.updates)
	require.Equal(t, cfg, manager.updatedConfig)
}

func TestEnsureStreamReturnsAddError(t *testing.T) {
	wantErr := errors.New("bad subjects")
	manager := &fakeStreamManager{addErr: wantErr}

	err := ensureStream(manager, &natslib.StreamConfig{Name: "MOOX_STORAGE"})

	require.ErrorIs(t, err, wantErr)
	require.Zero(t, manager.updates)
}

func TestDurableConsumerNameDerivesSubjectKind(t *testing.T) {
	require.Equal(t,
		"storage_deriver_time_series_rows_changed_v1",
		durableConsumerName("storage_deriver", "moox.storage.time_series.rows_changed.v1"),
	)
	require.Equal(t,
		"storage_deriver_record_rows_changed_v1",
		durableConsumerName("storage_deriver", "moox.storage.record.rows_changed.v1"),
	)
}

func TestDurableConsumerNameSanitizesBaseAndSubject(t *testing.T) {
	require.Equal(t,
		"storage_deriver_record_rows_changed_v1",
		durableConsumerName("", "moox.storage.record.rows_changed.v1"),
	)
	require.Equal(t,
		"storage_deriver_us_east_time_series_rows_changed_v1",
		durableConsumerName(" storage-deriver.us/east ", "moox.storage.time_series.rows_changed.v1"),
	)
}

// fakeStreamManager 是 NATS 生产者测试使用的流管理桩。
type fakeStreamManager struct {
	adds          int
	updates       int
	addErr        error
	updatedConfig *natslib.StreamConfig
}

func (m *fakeStreamManager) AddStream(cfg *natslib.StreamConfig, opts ...natslib.JSOpt) (*natslib.StreamInfo, error) {
	_ = cfg
	_ = opts
	m.adds++
	if m.addErr != nil {
		return nil, m.addErr
	}
	return &natslib.StreamInfo{}, nil
}

func (m *fakeStreamManager) UpdateStream(cfg *natslib.StreamConfig, opts ...natslib.JSOpt) (*natslib.StreamInfo, error) {
	_ = opts
	m.updates++
	m.updatedConfig = cfg
	return &natslib.StreamInfo{}, nil
}
