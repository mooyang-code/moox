package sqlite

import (
	"context"
	"testing"
)

func TestDeleteSpaceCascadesRichMetadataGraph(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO t_spaces(c_space_id,c_name) VALUES ('target','Target'),('keep','Keep');
		INSERT INTO t_data_nodes(c_node_id,c_name,c_service_target) VALUES ('node','Node','ip://127.0.0.1:1');
		INSERT INTO t_storage_devices(c_device_id,c_name,c_engine) VALUES ('device','Device','duckdb');
		INSERT INTO t_data_sources(c_space_id,c_data_source_id,c_name,c_kind) VALUES ('target','source','Source','internal');
		INSERT INTO t_subjects(c_space_id,c_subject_id,c_subject_type,c_name) VALUES ('target','subject','fund','Subject');
		INSERT INTO t_subject_symbols(c_space_id,c_subject_id,c_data_source_id,c_external_symbol)
			VALUES ('target','subject','source','TARGET');
		INSERT INTO t_field_groups(c_space_id,c_group_id,c_name) VALUES ('target','root','Root');
		INSERT INTO t_field_groups(c_space_id,c_group_id,c_name,c_parent_group_id) VALUES ('target','child','Child','root');
		INSERT INTO t_fields(c_space_id,c_field_id,c_group_id,c_name,c_value_type) VALUES ('target','value','child','Value','double');
		INSERT INTO t_datasets(c_space_id,c_dataset_id,c_data_source_id,c_data_node_id,c_name,c_data_kind,c_keep_duration)
			VALUES ('target','dataset','source','node','Dataset','time_series','24h');
		INSERT INTO t_dataset_subjects(c_space_id,c_dataset_id,c_subject_id) VALUES ('target','dataset','subject');
		INSERT INTO t_dataset_columns(c_space_id,c_dataset_id,c_column_name,c_origin_type,c_origin_id,c_value_type)
			VALUES ('target','dataset','value','field','value','double');
		INSERT INTO t_views(c_space_id,c_view_id,c_name,c_primary_dataset_id) VALUES ('target','view','View','dataset');
		INSERT INTO t_view_columns(c_space_id,c_view_id,c_column_name,c_origin_type,c_origin_id,c_value_type)
			VALUES ('target','view','value','dataset_column','dataset.value','double');
		INSERT INTO t_view_index_builds(
			c_space_id,c_view_id,c_build_id,c_index_id,c_engine,c_target_view_version,c_state,
			c_owner_id,c_new_slot,c_status,c_started_at,c_updated_at
		) VALUES ('target','view','build','index','duckdb',1,1,'owner','slot-b','building','now','now');
		INSERT INTO t_factors(c_space_id,c_factor_id,c_name,c_value_type) VALUES ('target','factor','Factor','double');
		INSERT INTO t_archive_files(c_space_id,c_archive_file_id,c_dataset_id,c_device_id,c_partition_key,c_file_uri)
			VALUES ('target','archive','dataset','device','2026-07-28','file:///archive.parquet');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSpace(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"t_spaces", "t_data_sources", "t_subjects", "t_subject_symbols",
		"t_field_groups", "t_fields", "t_datasets", "t_dataset_subjects",
		"t_dataset_columns", "t_views", "t_view_columns", "t_view_index_builds",
		"t_factors", "t_archive_files",
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE c_space_id = 'target'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d target rows", table, count)
		}
	}
	var keep int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_spaces WHERE c_space_id = 'keep'`).Scan(&keep); err != nil {
		t.Fatal(err)
	}
	if keep != 1 {
		t.Fatalf("unrelated space count = %d", keep)
	}
}
