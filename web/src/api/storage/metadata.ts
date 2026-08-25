import { callStorage as callMetadata } from "./http";
import type {
  ArchiveFile,
  DataNode,
  DataNodeListItem,
  DataSource,
  Dataset,
  DatasetActivationCheck,
  DatasetColumn,
  DatasetSubject,
  DatasetMutation,
  Factor,
  Field,
  FieldGroup,
  Page,
  PageResult,
  RetInfo,
  Subject,
  SubjectSymbol,
  View,
  ViewColumn,
  ViewRebuildLog
} from "./types";

type RetRsp = { ret_info: RetInfo };

export async function createDataSource(data_source: DataSource) {
  const rsp = await callMetadata<{ data_source: DataSource }, RetRsp & { data_source: DataSource }>("CreateDataSource", {
    data_source
  });
  return rsp.data_source;
}

export async function updateDataSource(data_source: DataSource) {
  const rsp = await callMetadata<{ data_source: DataSource }, RetRsp & { data_source: DataSource }>("UpdateDataSource", {
    data_source
  });
  return rsp.data_source;
}

export function getDataSource(params: { space_id: string; data_source_id: string }) {
  return callMetadata<typeof params, RetRsp & { data_source: DataSource }>("GetDataSource", params);
}

export function listDataSources(params: { space_id: string; kind?: string; status?: string; keyword?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { data_sources: DataSource[]; page_result: PageResult }>("ListDataSources", params);
}

export async function upsertSubject(subject: Subject) {
  const rsp = await callMetadata<{ subject: Subject }, RetRsp & { subject: Subject }>("UpsertSubject", { subject });
  return rsp.subject;
}

export function getSubject(params: { space_id: string; subject_id: string }) {
  return callMetadata<typeof params, RetRsp & { subject: Subject }>("GetSubject", params);
}

export function listSubjects(params: {
  space_id: string;
  subject_type?: string;
  market?: string;
  status?: string;
  keyword?: string;
  page?: Page;
}) {
  return callMetadata<typeof params, RetRsp & { subjects: Subject[]; page_result: PageResult }>("ListSubjects", params);
}

export async function upsertSubjectSymbol(subject_symbol: SubjectSymbol) {
  const rsp = await callMetadata<{ subject_symbol: SubjectSymbol }, RetRsp & { subject_symbol: SubjectSymbol }>(
    "UpsertSubjectSymbol",
    { subject_symbol }
  );
  return rsp.subject_symbol;
}

export function listSubjectSymbols(params: { space_id: string; subject_id?: string; data_source_id?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { subject_symbols: SubjectSymbol[]; page_result: PageResult }>(
    "ListSubjectSymbols",
    params
  );
}

export async function createDataset(dataset: Dataset) {
  const rsp = await callMetadata<{ dataset: Dataset }, RetRsp & { dataset: Dataset }>("CreateDataset", { dataset });
  return rsp.dataset;
}

export async function updateDataset(dataset: DatasetMutation) {
  const rsp = await callMetadata<{ dataset: DatasetMutation }, RetRsp & { dataset: Dataset }>("UpdateDataset", { dataset });
  return rsp.dataset;
}

export function getDataset(params: { space_id: string; dataset_id: string }) {
  return callMetadata<typeof params, RetRsp & { dataset: Dataset }>("GetDataset", params);
}

export function listDatasets(params: {
  space_id: string;
  data_source_id?: string;
  data_kind?: string;
  data_node_id?: string;
  page?: Page;
}) {
  return callMetadata<typeof params, RetRsp & { datasets: Dataset[]; page_result: PageResult }>("ListDatasets", params);
}

export async function bindDatasetSubject(dataset_subject: DatasetSubject) {
  const rsp = await callMetadata<{ dataset_subject: DatasetSubject }, RetRsp & { dataset_subject: DatasetSubject }>(
    "BindDatasetSubject",
    { dataset_subject }
  );
  return rsp.dataset_subject;
}

export function listDatasetSubjects(params: { space_id: string; dataset_id?: string; subject_id?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { dataset_subjects: DatasetSubject[]; page_result: PageResult }>(
    "ListDatasetSubjects",
    params
  );
}

export async function createField(field: Field) {
  const rsp = await callMetadata<{ field: Field }, RetRsp & { field: Field }>("CreateField", { field });
  return rsp.field;
}

export async function createFieldGroup(field_group: FieldGroup) {
  const rsp = await callMetadata<{ field_group: FieldGroup }, RetRsp & { field_group: FieldGroup }>("CreateFieldGroup", {
    field_group
  });
  return rsp.field_group;
}

export async function updateFieldGroup(field_group: FieldGroup) {
  const rsp = await callMetadata<{ field_group: FieldGroup }, RetRsp & { field_group: FieldGroup }>("UpdateFieldGroup", {
    field_group
  });
  return rsp.field_group;
}

export function listFieldGroups(params: { space_id: string; parent_group_id?: string; page?: Page }) {
  return callMetadata<
    typeof params,
    RetRsp & {
      field_groups: FieldGroup[];
      page_result: PageResult;
      field_counts?: Record<string, number>;
      total_field_count?: number;
      ungrouped_field_count?: number;
    }
  >("ListFieldGroups", params);
}

export async function updateField(field: Field) {
  const rsp = await callMetadata<{ field: Field }, RetRsp & { field: Field }>("UpdateField", { field });
  return rsp.field;
}

export function getField(params: { space_id: string; field_id: string }) {
  return callMetadata<typeof params, RetRsp & { field: Field }>("GetField", params);
}

export function listFields(params: {
  space_id: string;
  group_id?: string;
  value_type?: string | number;
  status?: string;
  keyword?: string;
  include_descendants?: boolean;
  ungrouped_only?: boolean;
  sort_by?: "sort_order" | "field_id" | "updated_at";
  sort_order?: "asc" | "desc";
  page?: Page;
}) {
  return callMetadata<typeof params, RetRsp & { fields: Field[]; page_result: PageResult }>("ListFields", params);
}

export function batchUpdateFields(params: {
  space_id: string;
  field_ids: string[];
  target_group_id?: string;
  target_status?: "active" | "disabled";
}) {
  return callMetadata<typeof params, RetRsp & { updated_count: number }>("BatchUpdateFields", params);
}

export function deleteFieldGroup(params: { space_id: string; group_id: string }) {
  return callMetadata<typeof params, RetRsp>("DeleteFieldGroup", params);
}

export async function createFactor(factor: Factor) {
  const rsp = await callMetadata<{ factor: Factor }, RetRsp & { factor: Factor }>("CreateFactor", { factor });
  return rsp.factor;
}

export async function updateFactor(factor: Factor) {
  const rsp = await callMetadata<{ factor: Factor }, RetRsp & { factor: Factor }>("UpdateFactor", { factor });
  return rsp.factor;
}

export function getFactor(params: { space_id: string; factor_id: string }) {
  return callMetadata<typeof params, RetRsp & { factor: Factor }>("GetFactor", params);
}

export function listFactors(params: { space_id: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { factors: Factor[]; page_result: PageResult }>("ListFactors", params);
}

export async function upsertDatasetColumn(dataset_column: DatasetColumn) {
  const rsp = await callMetadata<{ column: DatasetColumn }, RetRsp & { column: DatasetColumn }>("UpsertDatasetColumn", {
    column: dataset_column
  });
  return rsp.column;
}

export function listDatasetColumns(params: { space_id: string; dataset_id: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { columns: DatasetColumn[]; page_result: PageResult }>(
    "ListDatasetColumns",
    params
  );
}

export async function createView(view: View) {
  const rsp = await callMetadata<{ view: View }, RetRsp & { view: View }>("CreateView", { view });
  return rsp.view;
}

export async function updateView(view: View) {
  const rsp = await callMetadata<{ view: View }, RetRsp & { view: View }>("UpdateView", { view });
  return rsp.view;
}

export function requestViewRebuild(params: { space_id: string; view_id: string }) {
  return callMetadata<typeof params, RetRsp & { view: View }>("RequestViewRebuild", params);
}

export function getView(params: { space_id: string; view_id: string }) {
  return callMetadata<typeof params, RetRsp & { view: View }>("GetView", params);
}

export function listViews(params: { space_id: string; primary_dataset_id?: string; status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { views: View[]; page_result: PageResult }>("ListViews", params);
}

export async function upsertViewColumn(view_column: ViewColumn) {
  const rsp = await callMetadata<{ column: ViewColumn }, RetRsp & { column: ViewColumn }>("UpsertViewColumn", {
    column: view_column
  });
  return rsp.column;
}

export function listViewColumns(params: { space_id: string; view_id: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { columns: ViewColumn[]; page_result: PageResult }>("ListViewColumns", params);
}

export function listViewRebuildLogs(params: { space_id: string; view_id: string; result?: number; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { logs: ViewRebuildLog[]; page_result: PageResult }>("ListViewRebuildLogs", params);
}

export function getDataNode(params: { node_id: string }) {
  return callMetadata<typeof params, RetRsp & { node: DataNode }>("GetDataNode", params);
}

export function listDataNodes(params: { status?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { items: DataNodeListItem[]; page_result: PageResult }>("ListDataNodes", params);
}

export async function updateDataNode(params: { node_id: string; name: string; status: string }) {
  const rsp = await callMetadata<typeof params, RetRsp & { node: DataNode }>("UpdateDataNode", params);
  return rsp.node;
}

export function deleteDataNode(params: { node_id: string }) {
  return callMetadata<typeof params, RetRsp & { node: DataNode }>("DeleteDataNode", params);
}

export function checkDatasetActivation(params: { space_id: string; dataset_id: string }) {
  return callMetadata<
    typeof params,
    RetRsp & { dataset_revision: number | string; checks: DatasetActivationCheck[]; ready: boolean }
  >("CheckDatasetActivation", params);
}

export function activateDataset(params: { space_id: string; dataset_id: string; expected_revision: number | string }) {
  return callMetadata<typeof params, RetRsp & { dataset: Dataset; checks: DatasetActivationCheck[] }>("ActivateDataset", params);
}

export function rebindDatasetDataNode(params: {
  space_id: string;
  dataset_id: string;
  data_node_id: string;
  expected_revision: number | string;
}) {
  return callMetadata<typeof params, RetRsp & { dataset: Dataset }>("RebindDatasetDataNode", params);
}

export async function registerArchiveFile(archive_file: ArchiveFile) {
  const rsp = await callMetadata<{ archive_file: ArchiveFile }, RetRsp & { archive_file: ArchiveFile }>("RegisterArchiveFile", {
    archive_file
  });
  return rsp.archive_file;
}

export function listArchiveFiles(params: {
  space_id: string;
  dataset_id?: string;
  status?: string;
  sort_by?: "min_time" | "max_time" | "created_at" | "updated_at";
  sort_order?: "asc" | "desc";
  page?: Page;
}) {
  return callMetadata<typeof params, RetRsp & { archive_files: ArchiveFile[]; page_result: PageResult }>(
    "ListArchiveFiles",
    params
  );
}
