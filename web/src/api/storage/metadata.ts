import { callStorage as callMetadata } from "./http";
import type {
  ArchiveFile,
  DataNode,
  DataNodeListItem,
  DataSource,
  Dataset,
  DatasetColumn,
  Field,
  FieldGroup,
  Page,
  PageResult,
  RetInfo,
  Subject,
  Tag,
  TagMember,
  TagMemberStatus,
  TagReference,
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

export function listDataSources(params: { space_id: string; kind?: string; status?: string; keyword?: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { data_sources: DataSource[]; page_result: PageResult }>("ListDataSources", params);
}

export async function upsertSubject(subject: Subject) {
  const rsp = await callMetadata<{ subject: Subject }, RetRsp & { subject: Subject }>("UpsertSubject", { subject });
  return rsp.subject;
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

export async function upsertTag(tag: Tag, createOnly = false) {
  const rsp = await callMetadata<{ tag: Tag; create_only?: boolean }, RetRsp & { tag: Tag }>("UpsertTag", {
    tag,
    ...(createOnly ? { create_only: true } : {})
  });
  return rsp.tag;
}

export function listTags(spaceId: string, page: Page = { page: 1, size: 200 }) {
  return callMetadata<{ space_id: string; page: Page }, RetRsp & { tags: Tag[]; page_result: PageResult }>("ListTags", {
    space_id: spaceId,
    page
  });
}

export function deleteTag(spaceId: string, tagId: string) {
  return callMetadata<{ space_id: string; tag_id: string }, RetRsp & { references?: TagReference[] }>("DeleteTag", {
    space_id: spaceId,
    tag_id: tagId
  });
}

export function listTagMembers(params: { space_id: string; tag_id?: string; status?: string; keyword?: string; page: Page }) {
  return callMetadata<typeof params, RetRsp & { members: TagMember[]; page_result: PageResult }>("ListTagMembers", params);
}

export function addTagMembers(spaceId: string, tagId: string, subjectIds: string[]) {
  return callMetadata("AddTagMembers", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds });
}

export function removeTagMembers(spaceId: string, tagId: string, subjectIds: string[]) {
  return callMetadata("RemoveTagMembers", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds });
}

export function setTagMemberStatus(spaceId: string, tagId: string, subjectIds: string[], status: TagMemberStatus) {
  return callMetadata("SetTagMemberStatus", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds, status });
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

export function requestViewRebuild(params: { space_id: string; view_id: string }) {
  return callMetadata<typeof params, RetRsp & { view: View }>("RequestViewRebuild", params);
}

export function getView(params: { space_id: string; view_id: string }) {
  return callMetadata<typeof params, RetRsp & { view: View }>("GetView", params);
}

export function listViews(params: {
  space_id: string;
  dataset_id?: string;
  primary_dataset_id?: string;
  status?: string;
  page?: Page;
}) {
  const { primary_dataset_id, dataset_id, ...rest } = params;
  return callMetadata<typeof rest & { dataset_id?: string }, RetRsp & { views: View[]; page_result: PageResult }>("ListViews", {
    ...rest,
    ...(dataset_id || primary_dataset_id ? { dataset_id: dataset_id || primary_dataset_id } : {})
  });
}

export function listViewColumns(params: { space_id: string; view_id: string; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { columns: ViewColumn[]; page_result: PageResult }>("ListViewColumns", params);
}

export function listViewRebuildLogs(params: { space_id: string; view_id: string; result?: number; page?: Page }) {
  return callMetadata<typeof params, RetRsp & { logs: ViewRebuildLog[]; page_result: PageResult }>("ListViewRebuildLogs", params);
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
