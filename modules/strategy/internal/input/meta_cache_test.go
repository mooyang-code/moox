package input

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// datasetCountingClient 在 countingClient 之上统计 GetDataset 与 GetTag 的读取次数。
type datasetCountingClient struct {
	*countingClient
	datasets, tags int
}

func (c *datasetCountingClient) GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	c.datasets++
	return c.countingClient.GetDataset(ctx, spaceID, datasetID)
}

func (c *datasetCountingClient) GetTag(ctx context.Context, spaceID, tagID string) (TagInfo, error) {
	c.tags++
	return c.countingClient.GetTag(ctx, spaceID, tagID)
}

func readEventMetadata(t *testing.T, loader Loader) []Subject {
	t.Helper()
	event := loader.ForEvent()
	revalidator := event.Client.(interface {
		Revalidate(ctx context.Context, spaceID, datasetID string) error
	})
	for i := 0; i < 2; i++ { // 同一事件内多个实例：只校验一次
		if err := revalidator.Revalidate(context.Background(), "space", "ds_factor"); err != nil {
			t.Fatal(err)
		}
	}
	subjects, err := event.Client.ListDatasetSubjects(context.Background(), "space", "ds_factor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := event.Client.ListTagMembers(context.Background(), "space", "majors"); err != nil {
		t.Fatal(err)
	}
	if _, err := event.Client.GetTag(context.Background(), "space", "binance_spot"); err != nil {
		t.Fatal(err)
	}
	return subjects
}

// 修订号不变时跨事件复用标的元数据，变化后重读；每个事件只查一次修订号。
func TestMetaCacheReusesAcrossEventsUntilRevisionChanges(t *testing.T) {
	fake := newFakeClient("spot")
	dataset := fake.datasets["ds_factor"]
	dataset.SubjectsRevision = 7
	fake.datasets["ds_factor"] = dataset
	client := &datasetCountingClient{countingClient: &countingClient{fakeClient: fake}}
	loader := Loader{Client: client, Meta: NewMetaCache()}

	for i := 0; i < 3; i++ {
		readEventMetadata(t, loader)
	}
	if client.datasets != 3 || client.subjects != 1 || client.members != 1 || client.tags != 1 {
		t.Fatalf("修订号不变时应只在首个事件读取：datasets=%d subjects=%d members=%d tags=%d", client.datasets, client.subjects, client.members, client.tags)
	}

	// 标的集合变化：修订号递增后，下一事件重读且看到新数据。
	fake.subjects["ds_factor"] = append(fake.subjects["ds_factor"], subject("NEW-USDT", true))
	dataset.SubjectsRevision = 8
	fake.datasets["ds_factor"] = dataset
	subjects := readEventMetadata(t, loader)
	if client.subjects != 2 || client.members != 2 || client.tags != 2 {
		t.Fatalf("修订号变化后应重读：subjects=%d members=%d tags=%d", client.subjects, client.members, client.tags)
	}
	found := false
	for _, s := range subjects {
		found = found || s.SubjectID == "NEW-USDT"
	}
	if !found {
		t.Fatal("重读后应包含新增标的")
	}

	// 修订号回到旧值也视为变化（只要与缓存的不相等）。
	dataset.SubjectsRevision = 7
	fake.datasets["ds_factor"] = dataset
	readEventMetadata(t, loader)
	if client.subjects != 3 {
		t.Fatalf("修订号不相等就应重读：subjects=%d", client.subjects)
	}
}

// 读取修订号失败时错误原样返回，调用方按基础设施错误处理，不使用缓存。
func TestMetaCacheRevalidateFailureIsReturned(t *testing.T) {
	fake := newFakeClient("spot")
	loader := Loader{Client: &countingClient{fakeClient: fake}, Meta: NewMetaCache()}
	event := loader.ForEvent()
	err := event.Client.(interface {
		Revalidate(ctx context.Context, spaceID, datasetID string) error
	}).Revalidate(context.Background(), "space", "ds_missing")
	if err == nil || errors.Is(err, ErrStale) {
		t.Fatalf("数据集不存在应返回读取错误：%v", err)
	}
}

// 经真实 LoadBar 连续处理多个事件：标的元数据跨事件复用，修订号变化后重读，修订号读取失败时返回错误。
func TestLoadBarUsesMetaCacheAcrossEvents(t *testing.T) {
	fake := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), fake, "space", "view_factor_1h", parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	fake.rows = func(query Query) ([]Row, uint64, error) {
		return rowsAt(query.Start, map[string]map[string]float64{"ETH-USDT": {"close": 10, "ma_20": 9, "bias_q_20": 0.5, "quote_volume_mean_20": 3e6, "quote_volume_mean_q_20": 0.4}}), 9, nil
	}
	client := &datasetCountingClient{countingClient: &countingClient{fakeClient: fake}}
	loader := Loader{Client: client, Meta: NewMetaCache()}
	loadEvent := func() error {
		_, err := loader.ForEvent().LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
		return err
	}

	for i := 0; i < 3; i++ {
		if err := loadEvent(); err != nil {
			t.Fatal(err)
		}
	}
	if client.subjects != 1 || client.members != 1 {
		t.Fatalf("三个事件应只读一次标的元数据：subjects=%d members=%d", client.subjects, client.members)
	}
	if client.datasets != 3 {
		t.Fatalf("每个事件应校验一次修订号：datasets=%d", client.datasets)
	}

	dataset := fake.datasets["ds_factor"]
	dataset.SubjectsRevision++
	fake.datasets["ds_factor"] = dataset
	if err := loadEvent(); err != nil {
		t.Fatal(err)
	}
	if client.subjects != 2 || client.members != 2 {
		t.Fatalf("修订号变化后应重读：subjects=%d members=%d", client.subjects, client.members)
	}

	delete(fake.datasets, "ds_factor")
	subjectsBefore := client.subjects
	if err := loadEvent(); err == nil {
		t.Fatal("修订号读取失败时 LoadBar 应返回错误，而不是使用缓存")
	}
	if client.subjects != subjectsBefore {
		t.Fatal("修订号读取失败后不应继续读取标的元数据")
	}
}

// 年龄探针通过的标的跨周期缓存：第二期不再发探针查询；活动索引变化后重新探针。
func TestAgeProbeCacheSkipsQueryUntilIndexChanges(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, strings.Replace(exampleDSL, "universe:\n", "universe:\n  min_age_bars: 24\n", 1))
	resolved, program, err := Resolve(context.Background(), client, "space", "view_factor_1h", strategy)
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		if len(query.Columns) == 1 && query.Columns[0] == ageProbeColumn {
			// 探针查询：全部候选都有窗口内的行。
			return rowsAt(query.Start, map[string]map[string]float64{"BTC-USDT": {"close": 1}, "ETH-USDT": {"close": 1}, "SOL-USDT": {"close": 1}}), 11, nil
		}
		return rowsAt(query.Start, map[string]map[string]float64{"ETH-USDT": {"close": 10, "ma_20": 9, "bias_q_20": 0.5, "quote_volume_mean_20": 3e6, "quote_volume_mean_q_20": 0.4}}), 11, nil
	}
	loader := Loader{Client: client, Meta: NewMetaCache()}
	probes := func() int {
		count := 0
		for _, query := range client.queries {
			if len(query.Columns) == 1 && query.Columns[0] == ageProbeColumn {
				count++
			}
		}
		return count
	}
	load := func() {
		t.Helper()
		if _, err := loader.ForEvent().LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart}); err != nil {
			t.Fatalf("装配失败：%v", err)
		}
	}
	load()
	if probes() != 1 {
		t.Fatalf("第一期应发 1 次探针查询：%d", probes())
	}
	load()
	if probes() != 1 {
		t.Fatalf("已证明年龄足够的标的不应再探针：%d", probes())
	}
	view := client.views["view_factor_1h"]
	view.ActiveIndexID = "idx_b"
	client.views["view_factor_1h"] = view
	load()
	if probes() != 2 {
		t.Fatalf("活动索引变化后应重新探针：%d", probes())
	}
}
