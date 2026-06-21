import { callMetadata } from './http';
import type {
  ArchiveFile,
  DataSource,
  Dataset,
  DatasetColumn,
  DatasetSubject,
  Factor,
  Field,
  Page,
  PageResult,
  PrimaryStoreNode,
  PrimaryStoreRoute,
  RetInfo,
  Subject,
  SubjectSymbol,
  View,
  ViewColumn,
} from './types';

type RetRsp = { ret_info: RetInfo };

export async function createDataSource(data_source: DataSource) {
  const rsp = await callMetadata<{ data_source: DataSource }, RetRsp & { data_source: DataSource }>('CreateDataSource', { data_source });
  return rsp.data_source;
}

export async function updateDataSource(data_source: DataSource) {
  const rsp = await callMetadata<{ data_source: DataSource }, RetRsp & { data_source: DataSource }>('UpdateDataSource', { data_source });
  return rsp.data_source;
}

export function getDataSource(params: { space_id: string; data_source_id: string }) {
  return callMetadata<typeof params, RetRsp & { data_source: DataSource }>('GetDataSource', params);
}

export function listDataSources(params: { space_id: string; kind?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { data_sources: DataSource[]; page_result: PageResult }>('ListDataSources', params);
}

export async function upsertSubject(subject: Subject) {
  const rsp = await callMetadata<{ subject: Subject }, RetRsp & { subject: Subject }>('UpsertSubject', { subject });
  return rsp.subject;
}

export function getSubject(params: { space_id: string; subject_id: string }) {
  return callMetadata<typeof params, RetRsp & { subject: Subject }>('GetSubject', params);
}

export function listSubjects(params: { space_id: string; subject_type?: string; market?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { subjects: Subject[]; page_result: PageResult }>('ListSubjects', params);
}

export async function upsertSubjectSymbol(subject_symbol: SubjectSymbol) {
  const rsp = await callMetadata<{ subject_symbol: SubjectSymbol }, RetRsp & { subject_symbol: SubjectSymbol }>('UpsertSubjectSymbol', { subject_symbol });
  return rsp.subject_symbol;
}

export function listSubjectSymbols(params: { space_id: string; subject_id?: string; data_source_id?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { subject_symbols: SubjectSymbol[]; page_result: PageResult }>('ListSubjectSymbols', params);
}

export async function createDataset(dataset: Dataset) {
  const rsp = await callMetadata<{ dataset: Dataset }, RetRsp & { dataset: Dataset }>('CreateDataset', { dataset });
  return rsp.dataset;
}

export async function updateDataset(dataset: Dataset) {
  const rsp = await callMetadata<{ dataset: Dataset }, RetRsp & { dataset: Dataset }>('UpdateDataset', { dataset });
  return rsp.dataset;
}

export function getDataset(params: { space_id: string; dataset_id: string }) {
  return callMetadata<typeof params, RetRsp & { dataset: Dataset }>('GetDataset', params);
}

export function listDatasets(params: { space_id: string; data_kind?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { datasets: Dataset[]; page_result: PageResult }>('ListDatasets', params);
}

export async function bindDatasetSubject(dataset_subject: DatasetSubject) {
  const rsp = await callMetadata<{ dataset_subject: DatasetSubject }, RetRsp & { dataset_subject: DatasetSubject }>('BindDatasetSubject', { dataset_subject });
  return rsp.dataset_subject;
}

export function listDatasetSubjects(params: { space_id: string; dataset_id?: string; subject_id?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { dataset_subjects: DatasetSubject[]; page_result: PageResult }>('ListDatasetSubjects', params);
}

export async function createField(field: Field) {
  const rsp = await callMetadata<{ field: Field }, RetRsp & { field: Field }>('CreateField', { field });
  return rsp.field;
}

export async function updateField(field: Field) {
  const rsp = await callMetadata<{ field: Field }, RetRsp & { field: Field }>('UpdateField', { field });
  return rsp.field;
}

export function getField(params: { space_id: string; field_id: string }) {
  return callMetadata<typeof params, RetRsp & { field: Field }>('GetField', params);
}

export function listFields(params: { space_id: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { fields: Field[]; page_result: PageResult }>('ListFields', params);
}

export async function createFactor(factor: Factor) {
  const rsp = await callMetadata<{ factor: Factor }, RetRsp & { factor: Factor }>('CreateFactor', { factor });
  return rsp.factor;
}

export async function updateFactor(factor: Factor) {
  const rsp = await callMetadata<{ factor: Factor }, RetRsp & { factor: Factor }>('UpdateFactor', { factor });
  return rsp.factor;
}

export function getFactor(params: { space_id: string; factor_id: string }) {
  return callMetadata<typeof params, RetRsp & { factor: Factor }>('GetFactor', params);
}

export function listFactors(params: { space_id: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { factors: Factor[]; page_result: PageResult }>('ListFactors', params);
}

export async function upsertDatasetColumn(dataset_column: DatasetColumn) {
  const rsp = await callMetadata<{ column: DatasetColumn }, RetRsp & { column: DatasetColumn }>('UpsertDatasetColumn', { column: dataset_column });
  return rsp.column;
}

export function listDatasetColumns(params: { space_id: string; dataset_id: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { columns: DatasetColumn[]; page_result: PageResult }>('ListDatasetColumns', params);
}

export async function createView(view: View) {
  const rsp = await callMetadata<{ view: View }, RetRsp & { view: View }>('CreateView', { view });
  return rsp.view;
}

export async function updateView(view: View) {
  const rsp = await callMetadata<{ view: View }, RetRsp & { view: View }>('UpdateView', { view });
  return rsp.view;
}

export function getView(params: { space_id: string; view_id: string }) {
  return callMetadata<typeof params, RetRsp & { view: View }>('GetView', params);
}

export function listViews(params: { space_id: string; primary_dataset_id?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { views: View[]; page_result: PageResult }>('ListViews', params);
}

export async function upsertViewColumn(view_column: ViewColumn) {
  const rsp = await callMetadata<{ column: ViewColumn }, RetRsp & { column: ViewColumn }>('UpsertViewColumn', { column: view_column });
  return rsp.column;
}

export function listViewColumns(params: { space_id: string; view_id: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { columns: ViewColumn[]; page_result: PageResult }>('ListViewColumns', params);
}

export async function createPrimaryStoreNode(primary_store_node: PrimaryStoreNode) {
  const rsp = await callMetadata<{ node: PrimaryStoreNode }, RetRsp & { node: PrimaryStoreNode }>('CreatePrimaryStoreNode', { node: primary_store_node });
  return rsp.node;
}

export async function updatePrimaryStoreNode(primary_store_node: PrimaryStoreNode) {
  const rsp = await callMetadata<{ node: PrimaryStoreNode }, RetRsp & { node: PrimaryStoreNode }>('UpdatePrimaryStoreNode', { node: primary_store_node });
  return rsp.node;
}

export function getPrimaryStoreNode(params: { node_id: string }) {
  return callMetadata<typeof params, RetRsp & { node: PrimaryStoreNode }>('GetPrimaryStoreNode', params);
}

export function listPrimaryStoreNodes(params: { status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { nodes: PrimaryStoreNode[]; page_result: PageResult }>('ListPrimaryStoreNodes', params);
}

export async function createPrimaryStoreRoute(primary_store_route: PrimaryStoreRoute) {
  const rsp = await callMetadata<{ primary_store_route: PrimaryStoreRoute }, RetRsp & { primary_store_route: PrimaryStoreRoute }>('CreatePrimaryStoreRoute', { primary_store_route });
  return rsp.primary_store_route;
}

export async function updatePrimaryStoreRoute(primary_store_route: PrimaryStoreRoute) {
  const rsp = await callMetadata<{ primary_store_route: PrimaryStoreRoute }, RetRsp & { primary_store_route: PrimaryStoreRoute }>('UpdatePrimaryStoreRoute', { primary_store_route });
  return rsp.primary_store_route;
}

export function getPrimaryStoreRoute(params: { space_id: string; route_id: string }) {
  return callMetadata<typeof params, RetRsp & { primary_store_route: PrimaryStoreRoute }>('GetPrimaryStoreRoute', params);
}

export function listPrimaryStoreRoutes(params: { space_id: string; dataset_id?: string; subject_id?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { primary_store_routes: PrimaryStoreRoute[]; page_result: PageResult }>('ListPrimaryStoreRoutes', params);
}

export async function registerArchiveFile(archive_file: ArchiveFile) {
  const rsp = await callMetadata<{ archive_file: ArchiveFile }, RetRsp & { archive_file: ArchiveFile }>('RegisterArchiveFile', { archive_file });
  return rsp.archive_file;
}

export function listArchiveFiles(params: { space_id: string; dataset_id?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { archive_files: ArchiveFile[]; page_result: PageResult }>('ListArchiveFiles', params);
}
