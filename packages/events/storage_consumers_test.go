package events

import "testing"

func TestStorageViewConsumerNameIsStableAndRecognized(t *testing.T) {
	name := StorageViewConsumerName("crypto", "dataset_a")
	if name != StorageViewConsumerName(" crypto ", " dataset_a ") || name == StorageViewConsumerName("crypto", "dataset_b") || name == StorageViewConsumerName("stockcn", "dataset_a") {
		t.Fatalf("消费者名必须由（空间，数据集）稳定决定：%s", name)
	}
	if !IsStorageViewDurable(name) || len(name) != len("view_")+12 {
		t.Fatalf("按数据集生成的消费者名应被认可：%s", name)
	}
}

func TestIsStorageViewDurable(t *testing.T) {
	for _, name := range []string{"view_0123456789ab", "view_ffffffffffff"} {
		if !IsStorageViewDurable(name) {
			t.Fatalf("View 消费者 %s 应被认可", name)
		}
	}
	for _, name := range []string{
		"", "view", "view_", "view_0123456789a", "view_0123456789abc", "view_0123456789AB", "view_0123456789ag",
		"view_01234567.9ab", "view_*", "*", ">", "storage_view_misc_0123456789ab", "storage_view_factor_0123456789ab",
		"factor_collector_period_v1", "other_view_0123456789ab",
	} {
		if IsStorageViewDurable(name) {
			t.Fatalf("%q 不应被认可", name)
		}
	}
}

func TestIsStorageDatasetSubject(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range StorageDatasetEvents {
		subject, err := registry.RenderSubject(event, "mooxsys", "kline.1m")
		if err != nil {
			t.Fatal(err)
		}
		if !IsStorageDatasetSubject(subject) {
			t.Fatalf("按注册表渲染出的主题应被认可: %s", subject)
		}
	}
	viewReady, err := registry.RenderSubject(ViewDataReady, "mooxsys", "kline.1m")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{
		"",
		">",
		"moox.>",
		"moox.event.storage.dataset.rows.upserted.v2.>",
		"moox.event.storage.dataset.rows.upserted.v2.*.*",
		"moox.event.storage.dataset.rows.upserted.v2.mfrxg4tt",
		"moox.event.storage.dataset.rows.upserted.v2.mfrxg4tt.mfrxg4tt.mfrxg4tt",
		"moox.event.storage.dataset.rows.upserted.v2..mfrxg4tt",
		"moox.event.storage.dataset.rows.upserted.v2.MFRXG4TT.mfrxg4tt",
		"moox.event.storage.dataset.rows.upserted.v1.mfrxg4tt.mfrxg4tt",
		"moox.event.observability.host.snapshot.reported.v1.mfrxg4tt.mfrxg4tt",
		viewReady, // 数据就绪事件不在可清除范围内
	} {
		if IsStorageDatasetSubject(subject) {
			t.Fatalf("%q 不应被认可", subject)
		}
	}
}
