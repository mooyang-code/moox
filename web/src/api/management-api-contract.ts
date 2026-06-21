import { createSpace, listSpaceMembers, listSpaces, updateSpace } from './control/spaces';
import type { Space } from './control/types';
import {
  bindDatasetSubject,
  createDataSource,
  createDataset,
  createFactor,
  createField,
  createPrimaryStoreNode,
  createPrimaryStoreRoute,
  createView,
  getDataset,
  getDataSource,
  getFactor,
  getField,
  getPrimaryStoreNode,
  getPrimaryStoreRoute,
  getSubject,
  getView,
  listArchiveFiles,
  listDatasetColumns,
  listDatasets,
  listDatasetSubjects,
  listDataSources,
  listFactors,
  listFields,
  listPrimaryStoreNodes,
  listPrimaryStoreRoutes,
  listSubjectSymbols,
  listSubjects,
  listViewColumns,
  listViews,
  registerArchiveFile,
  updateDataSource,
  updateDataset,
  updateFactor,
  updateField,
  updatePrimaryStoreNode,
  updatePrimaryStoreRoute,
  updateView,
  upsertDatasetColumn,
  upsertSubject,
  upsertSubjectSymbol,
  upsertViewColumn,
} from './storage/metadata';
import { readRecordRows, readTimeSeriesRows, writeRecordRows, writeTimeSeriesRows } from './storage/access';
import { queryTimeSeriesRows, rebuildRecordView, rebuildTimeSeriesView, searchRecordRows } from './storage/view';
import type { ColumnValue, Dataset, Field, PrimaryStoreNode, PrimaryStoreRoute, RecordRow, TimeSeriesRow } from './storage/types';

const sampleSpace: Space = {
  space_id: 'contract-space',
  name: 'Contract Space',
  status: 'active',
};

const sampleDataset: Dataset = {
  space_id: sampleSpace.space_id,
  dataset_id: 'kline',
  data_source_id: 'binance',
  name: 'Kline',
  data_kind: 'DATA_KIND_TIME_SERIES',
  freqs: ['1m'],
  status: 'active',
};

const sampleField: Field = {
  space_id: sampleSpace.space_id,
  field_id: 'close',
  name: 'Close',
  value_type: 'FIELD_VALUE_TYPE_DOUBLE',
  status: 'active',
};

const sampleColumn: ColumnValue = {
  column_name: 'close',
  value_type: 'FIELD_VALUE_TYPE_DOUBLE',
  value: { double_value: 1 },
};

const sampleTimeSeriesRow: TimeSeriesRow = {
  key: {
    space_id: sampleSpace.space_id,
    dataset_id: sampleDataset.dataset_id,
    subject_id: 'BTC-USDT',
    freq: '1m',
    data_time: '2026-01-01T00:00:00Z',
  },
  columns: [sampleColumn],
};

const sampleRecordRow: RecordRow = {
  key: {
    space_id: sampleSpace.space_id,
    dataset_id: 'news',
    record_id: 'news-1',
  },
  columns: [sampleColumn],
};

const sampleNode: PrimaryStoreNode = {
  node_id: 'primary-local',
  name: 'Local Primary',
  endpoint: 'local',
  weight: 100,
  status: 'active',
};

const sampleRoute: PrimaryStoreRoute = {
  space_id: sampleSpace.space_id,
  route_id: 'route-kline',
  dataset_id: sampleDataset.dataset_id,
  node_id: sampleNode.node_id,
  priority: 100,
  status: 'active',
};

export async function assertManagementApiContract() {
  await listSpaces();
  await createSpace(sampleSpace);
  await updateSpace(sampleSpace);
  await listSpaceMembers({ space_id: sampleSpace.space_id });

  await createDataSource({ space_id: sampleSpace.space_id, data_source_id: 'binance', name: 'Binance', kind: 'exchange', status: 'active' });
  await updateDataSource({ space_id: sampleSpace.space_id, data_source_id: 'binance', name: 'Binance', kind: 'exchange', status: 'active' });
  await getDataSource({ space_id: sampleSpace.space_id, data_source_id: 'binance' });
  await listDataSources({ space_id: sampleSpace.space_id });

  await upsertSubject({ space_id: sampleSpace.space_id, subject_id: 'BTC-USDT', subject_type: 'crypto_pair', name: 'BTC/USDT', status: 'active' });
  await getSubject({ space_id: sampleSpace.space_id, subject_id: 'BTC-USDT' });
  await listSubjects({ space_id: sampleSpace.space_id });
  await upsertSubjectSymbol({ space_id: sampleSpace.space_id, subject_id: 'BTC-USDT', data_source_id: 'binance', external_symbol: 'BTCUSDT', status: 'active' });
  await listSubjectSymbols({ space_id: sampleSpace.space_id, subject_id: 'BTC-USDT' });

  await createDataset(sampleDataset);
  await updateDataset(sampleDataset);
  await getDataset({ space_id: sampleSpace.space_id, dataset_id: sampleDataset.dataset_id });
  await listDatasets({ space_id: sampleSpace.space_id });
  await bindDatasetSubject({ space_id: sampleSpace.space_id, dataset_id: sampleDataset.dataset_id, subject_id: 'BTC-USDT', subject_role: 'normal', status: 'active' });
  await listDatasetSubjects({ space_id: sampleSpace.space_id, dataset_id: sampleDataset.dataset_id });

  await createField(sampleField);
  await updateField(sampleField);
  await getField({ space_id: sampleSpace.space_id, field_id: sampleField.field_id });
  await listFields({ space_id: sampleSpace.space_id });

  await createFactor({ space_id: sampleSpace.space_id, factor_id: 'ma20', name: 'MA20', algorithm: 'MA', value_type: 'FIELD_VALUE_TYPE_DOUBLE', status: 'active' });
  await updateFactor({ space_id: sampleSpace.space_id, factor_id: 'ma20', name: 'MA20', algorithm: 'MA', value_type: 'FIELD_VALUE_TYPE_DOUBLE', status: 'active' });
  await getFactor({ space_id: sampleSpace.space_id, factor_id: 'ma20' });
  await listFactors({ space_id: sampleSpace.space_id });

  await upsertDatasetColumn({ space_id: sampleSpace.space_id, dataset_id: sampleDataset.dataset_id, column_name: 'close', origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD', origin_id: 'close', value_type: 'FIELD_VALUE_TYPE_DOUBLE', status: 'active' });
  await listDatasetColumns({ space_id: sampleSpace.space_id, dataset_id: sampleDataset.dataset_id });

  await createView({ space_id: sampleSpace.space_id, view_id: 'kline_view', name: 'Kline View', primary_dataset_id: sampleDataset.dataset_id, dataset_ids: [sampleDataset.dataset_id], status: 'active' });
  await updateView({ space_id: sampleSpace.space_id, view_id: 'kline_view', name: 'Kline View', primary_dataset_id: sampleDataset.dataset_id, dataset_ids: [sampleDataset.dataset_id], status: 'active' });
  await getView({ space_id: sampleSpace.space_id, view_id: 'kline_view' });
  await listViews({ space_id: sampleSpace.space_id });
  await upsertViewColumn({ space_id: sampleSpace.space_id, view_id: 'kline_view', column_name: 'close', origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN', origin_id: 'kline.close', value_type: 'FIELD_VALUE_TYPE_DOUBLE' });
  await listViewColumns({ space_id: sampleSpace.space_id, view_id: 'kline_view' });

  await createPrimaryStoreNode(sampleNode);
  await updatePrimaryStoreNode(sampleNode);
  await getPrimaryStoreNode({ node_id: sampleNode.node_id });
  await listPrimaryStoreNodes({});

  await createPrimaryStoreRoute(sampleRoute);
  await updatePrimaryStoreRoute(sampleRoute);
  await getPrimaryStoreRoute({ space_id: sampleSpace.space_id, route_id: sampleRoute.route_id });
  await listPrimaryStoreRoutes({ space_id: sampleSpace.space_id });

  await registerArchiveFile({ space_id: sampleSpace.space_id, archive_file_id: 'archive-1', dataset_id: sampleDataset.dataset_id, device_id: 'parquet', file_uri: 'file:///tmp/archive.parquet', file_format: 'parquet', status: 'active' });
  await listArchiveFiles({ space_id: sampleSpace.space_id, dataset_id: sampleDataset.dataset_id });

  await writeTimeSeriesRows([sampleTimeSeriesRow]);
  await readTimeSeriesRows({ keys: [sampleTimeSeriesRow.key], time_range: { start_time: sampleTimeSeriesRow.key.data_time, end_time: sampleTimeSeriesRow.key.data_time } });
  await writeRecordRows([sampleRecordRow]);
  await readRecordRows({ keys: [sampleRecordRow.key] });

  await queryTimeSeriesRows({ space_id: sampleSpace.space_id, view_id: 'kline_view', keys: [sampleTimeSeriesRow.key] });
  await searchRecordRows({ space_id: sampleSpace.space_id, view_id: 'news_view', keys: [sampleRecordRow.key], text_query: 'btc' });
  await rebuildTimeSeriesView({ space_id: sampleSpace.space_id, view_id: 'kline_view' });
  await rebuildRecordView({ space_id: sampleSpace.space_id, view_id: 'news_view' });
}
