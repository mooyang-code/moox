package events

import "testing"

func TestIsStorageViewDurable(t *testing.T) {
	for _, name := range append([]string{"storage_view_misc_0123456789ab", "storage_view_factor_ffffffffffff"}, StorageViewConsumerDurables...) {
		if !IsStorageViewDurable(name) {
			t.Fatalf("View 消费者 %s 应被认可", name)
		}
	}
	for _, name := range []string{
		"", "storage_view", "factor_collector_period_v1", "*", ">", "storage_view_*",
		"storage_view_misc_", "storage_view_misc_0123456789a", "storage_view_misc_0123456789abc",
		"storage_view_misc_0123456789AB", "storage_view_misc_0123456789ag", "storage_view_misc_01234567.9ab",
		"storage_view_kline_0123456789ab", "other_storage_view_misc_0123456789ab",
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
