package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/server"
)

type fakeSubjectSyncStore struct {
	mu          sync.Mutex
	registered  map[string][]string
	registerErr error
	tags        []*pb.Tag
	listCalls   atomic.Int32
	listEntered chan struct{}
	listRelease chan struct{}
	attributes  map[string]int
}

func (s *fakeSubjectSyncStore) RegisterSubjectListing(_ context.Context, supported map[string][]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = supported
	return s.registerErr
}

func (s *fakeSubjectSyncStore) ListTags(context.Context) ([]*pb.Tag, error) {
	s.listCalls.Add(1)
	if s.listEntered != nil {
		s.listEntered <- struct{}{}
		<-s.listRelease
	}
	return s.tags, nil
}

func (*fakeSubjectSyncStore) ApplyTagSnapshot(context.Context, string, string, time.Time, []*pb.TagSnapshotItem) error {
	return nil
}

func (*fakeSubjectSyncStore) ReportTagRunFailure(context.Context, string, string, time.Time, string) error {
	return nil
}

func (s *fakeSubjectSyncStore) UpdateSubjectAttributes(_ context.Context, spaceID string, items []*pb.SubjectAttributes) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attributes == nil {
		s.attributes = map[string]int{}
	}
	s.attributes[spaceID] = len(items)
	return len(items), 0, nil
}

type staticSubjectLister []marketdata.Instrument

func (l staticSubjectLister) List(context.Context) ([]marketdata.Instrument, error) { return l, nil }

func testSubjectListers() subjectsync.Listers {
	return subjectsync.Listers{
		{Source: "binance", InstrumentType: "spot"}: staticSubjectLister{{SubjectID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT"}},
		{Source: "binance", InstrumentType: "swap"}: staticSubjectLister{{SubjectID: "ETH-USDT"}},
	}
}

func TestRegisterSubjectSyncRegistersListingBeforeTimers(t *testing.T) {
	store := &fakeSubjectSyncStore{}
	err := registerSubjectSync(context.Background(), &server.Server{}, SubjectSyncConfig{FetchTimeout: time.Second}, store, testSubjectListers(), nil)
	require.ErrorContains(t, err, "缺少定时器")
	require.Equal(t, map[string][]string{"binance": {"spot", "swap"}}, store.registered, "启动时先向 Storage 登记支持的数据源")

	store = &fakeSubjectSyncStore{registerErr: errors.New("storage unavailable")}
	err = registerSubjectSync(context.Background(), &server.Server{}, SubjectSyncConfig{FetchTimeout: time.Second}, store, testSubjectListers(), nil)
	require.ErrorContains(t, err, "登记")
}

// 一次标签同步超过 1 分钟时，下一次触发被跳过，不会并发执行。
func TestSubjectTagJobSkipsOverlappingTrigger(t *testing.T) {
	store := &fakeSubjectSyncStore{listEntered: make(chan struct{}), listRelease: make(chan struct{})}
	tags := &subjectsync.TagRunner{Store: store, Listers: testSubjectListers()}
	tagJob, _, err := subjectSyncJobs(tags, &subjectsync.AttributeRunner{}, time.Now)
	require.NoError(t, err)

	firstDone := make(chan error, 1)
	go func() { firstDone <- tagJob.Handle(context.Background()) }()
	<-store.listEntered
	require.NoError(t, tagJob.Handle(context.Background()), "重叠的触发直接跳过")
	close(store.listRelease)
	require.NoError(t, <-firstDone)
	require.EqualValues(t, 1, store.listCalls.Load())
}

// 属性同步按触发时刻判断是否到点：北京时间 08:10:45 触发时执行。
func TestSubjectAttributeJobUsesTriggerTime(t *testing.T) {
	store := &fakeSubjectSyncStore{}
	attributes := &subjectsync.AttributeRunner{
		Store: store, Listers: testSubjectListers(),
		Jobs: []subjectsync.AttributeJob{{SpaceID: "crypto", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "10 8 * * *", Timezone: "Asia/Shanghai"}},
	}
	trigger := time.Date(2026, 10, 9, 0, 10, 45, 0, time.UTC)
	_, attributeJob, err := subjectSyncJobs(&subjectsync.TagRunner{Store: store}, attributes, func() time.Time { return trigger })
	require.NoError(t, err)
	require.NoError(t, attributeJob.Handle(context.Background()))
	require.Equal(t, 1, store.attributes["crypto"])

	trigger = trigger.Add(time.Minute)
	store.attributes = nil
	require.NoError(t, attributeJob.Handle(context.Background()))
	require.Empty(t, store.attributes, "下一分钟不再执行")
}

// 两个标的同步定时器错开 schedule（第 0 秒）：标签在第 30 秒，属性在第 45 秒，超时都是 10 分钟。
func TestSubjectSyncTimersInTRPCConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	var config struct {
		Server struct {
			Service []struct {
				Name     string `yaml:"name"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
				Timeout  int    `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &config))
	want := map[string]string{subjectTagsTimerService: "30 * * * * *", subjectAttributesTimerService: "45 * * * * *"}
	for _, service := range config.Server.Service {
		cron, ok := want[service.Name]
		if !ok {
			continue
		}
		require.Equal(t, cron, service.Network, service.Name)
		require.Equal(t, "timer", service.Protocol, service.Name)
		require.Equal(t, int(subjectSyncTimeout/time.Millisecond), service.Timeout, service.Name)
		delete(want, service.Name)
	}
	require.Empty(t, want, "trpc_go.yaml 缺少标的同步定时器")
}
