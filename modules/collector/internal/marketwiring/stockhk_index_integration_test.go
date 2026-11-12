package marketwiring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	hktencent "github.com/mooyang-code/moox/modules/collector/internal/sources/stockhk/tencent"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

var hsiKey = subjectsync.IndexListerKey{MarketID: "stockhk", Source: "tencent", Index: "HSI"}

// hsiIndexListers 先断言装配出的指数成分表里有（stockhk, tencent, HSI），再把这一项换成用假 HTTP 客户端构造的
// 同一个数据源（目录登记的实例自己建客户端，测试里不出网）：请求按 offset 返回录下的真实返回
// （sources/stockhk/tencent/testdata），pageFile 可以替换某一页。
func hsiIndexListers(t *testing.T, pageFile func(offset string) string) (subjectsync.Listers, subjectsync.IndexListers) {
	t.Helper()
	listers, indexes, err := NewSubjectListers()
	require.NoError(t, err)
	require.Contains(t, indexes, hsiKey)
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		query := req.URL.Query()
		if req.URL.Host != "proxy.finance.qq.com" || query.Get("board_type") != "hkHSI" || query.Get("count") != "20" {
			recorder.WriteHeader(http.StatusNotFound)
			return recorder.Result(), nil
		}
		body, err := os.ReadFile(filepath.Join("..", "sources", "stockhk", "tencent", "testdata", pageFile(query.Get("offset"))))
		if err != nil {
			recorder.WriteHeader(http.StatusInternalServerError)
			return recorder.Result(), nil
		}
		_, _ = recorder.Write(body)
		return recorder.Result(), nil
	})}
	indexes[hsiKey] = indexLister{fetcher: hktencent.New(hktencent.Config{HTTPClient: client}), index: hsiKey.Index}
	return listers, indexes
}

// hkTagStorage 是假的 Storage，照 Storage 应用 auto 标签成员快照的做法（crud_tag_member.go 的 ApplyTagSnapshot）：
// 快照里还不存在的标的先创建，再成为成员；已存在的标的不改。
type hkTagStorage struct {
	tags     []*pb.Tag
	subjects map[string]*pb.Subject
	members  map[string][]string
}

func (s *hkTagStorage) ListTags(context.Context) ([]*pb.Tag, error) { return s.tags, nil }

func (s *hkTagStorage) ApplyTagSnapshot(_ context.Context, spaceID, tagID string, items []*pb.TagSnapshotItem) error {
	members := make([]string, 0, len(items))
	for _, item := range items {
		if _, exists := s.subjects[item.GetSubjectId()]; !exists {
			s.subjects[item.GetSubjectId()] = &pb.Subject{SpaceId: spaceID, SubjectId: item.GetSubjectId(), Name: item.GetName(), Timezone: item.GetTimezone(), Status: "active", Attributes: item.GetAttributes()}
		}
		members = append(members, item.GetSubjectId())
	}
	s.members[tagID] = members
	return nil
}

// hsiTag 是港股恒指标签的写法：sync.source 是采集用的行情源（东方财富），成分来源另写 sync.index_source（腾讯）。
func hsiTag() *pb.Tag {
	return &pb.Tag{SpaceId: "stockhk", TagId: "hk_hsi", Mode: "auto", Attributes: map[string]string{
		"market_type": "equity", "sync.source": "eastmoney", "sync.index_source": "tencent", "sync.index": "HSI",
	}}
}

// 标签同步对（stockhk, tencent, HSI）能取到成员；港股没有全市场标的同步，成分快照里带着创建标的要用的
// 名称、时区、属性，Storage 应用快照时创建还不存在的标的。
func TestTagSyncAppliesHSIConstituentsFromTencent(t *testing.T) {
	listers, indexes := hsiIndexListers(t, func(offset string) string { return "hsi_offset_" + offset + ".json" })
	for key := range listers {
		require.NotEqual(t, "stockhk", key.MarketID, "港股不提供全部标的")
	}

	// 库里只有腾讯控股一只标的（名称是旧的），其余 94 只都还不存在。
	storage := &hkTagStorage{
		tags:     []*pb.Tag{hsiTag()},
		subjects: map[string]*pb.Subject{"00700.XHKG": {SpaceId: "stockhk", SubjectId: "00700.XHKG", Name: "腾讯"}},
		members:  map[string][]string{},
	}
	runner := &subjectsync.TagRunner{Store: storage, Listers: listers, IndexListers: indexes}
	results, err := runner.SyncTags(context.Background(), subjectsync.Filter{SpaceID: "stockhk"})
	require.NoError(t, err)
	require.Equal(t, []subjectsync.TagResult{{SpaceID: "stockhk", TagID: "hk_hsi", Status: subjectsync.StatusSuccess, Subjects: 95}}, results)

	require.Len(t, storage.members["hk_hsi"], 95)
	require.Contains(t, storage.members["hk_hsi"], "00700.XHKG")
	require.Len(t, storage.subjects, 95, "不存在的 94 只在应用快照时创建")
	created := storage.subjects["00388.XHKG"]
	require.NotNil(t, created)
	require.Equal(t, "stockhk", created.GetSpaceId())
	require.Equal(t, "香港交易所", created.GetName())
	require.Equal(t, "Asia/Hong_Kong", created.GetTimezone())
	require.Equal(t, map[string]string{"subject_type": "stock", "market": "XHKG", "currency": "HKD", "exchange": "XHKG"}, created.GetAttributes())
	require.Equal(t, "腾讯", storage.subjects["00700.XHKG"].GetName(), "已存在的标的不改")

	// 成员的标的 ID 都能按港股的标的规则换成传给数据源的代码。
	for _, subjectID := range storage.members["hk_hsi"] {
		code, err := marketfetch.DefaultProviderSymbol("stockhk", "equity", subjectID)
		require.NoError(t, err, subjectID)
		require.Equal(t, subjectID[:5], code)
	}
}

// 不写 sync.index_source 时成分来源等于 sync.source：东方财富没有登记恒指成分，同步失败，不会去用腾讯。
func TestTagSyncHSINeedsIndexSource(t *testing.T) {
	_, indexes := hsiIndexListers(t, func(offset string) string { return "hsi_offset_" + offset + ".json" })
	tag := hsiTag()
	delete(tag.Attributes, "sync.index_source")
	storage := &hkTagStorage{tags: []*pb.Tag{tag}, subjects: map[string]*pb.Subject{}, members: map[string][]string{}}
	runner := &subjectsync.TagRunner{Store: storage, IndexListers: indexes, RetryDelays: make([]time.Duration, 0)}
	results, err := runner.SyncTags(context.Background(), subjectsync.Filter{SpaceID: "stockhk"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, subjectsync.StatusFailed, results[0].Status)
	require.Contains(t, results[0].Message, "eastmoney")
	require.Empty(t, storage.members)
}

// 名单不完整（第三页为空）时同步失败，不应用快照：标签保留原成员。
func TestTagSyncKeepsHSIMembersWhenConstituentsIncomplete(t *testing.T) {
	_, indexes := hsiIndexListers(t, func(offset string) string {
		if offset == "40" {
			return "hsi_offset_100.json"
		}
		return "hsi_offset_" + offset + ".json"
	})
	storage := &hkTagStorage{tags: []*pb.Tag{hsiTag()}, subjects: map[string]*pb.Subject{}, members: map[string][]string{"hk_hsi": {"00005.XHKG"}}}
	runner := &subjectsync.TagRunner{Store: storage, IndexListers: indexes, RetryDelays: make([]time.Duration, 0)}
	results, err := runner.SyncTags(context.Background(), subjectsync.Filter{SpaceID: "stockhk"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, subjectsync.StatusFailed, results[0].Status)
	require.Contains(t, results[0].Message, "offset=40 这一页为空")
	require.Equal(t, []string{"00005.XHKG"}, storage.members["hk_hsi"])
	require.Empty(t, storage.subjects)
}
