import { call, type CallOptions } from "@/api/http";
import { readAllPages } from "@/api/read-all-pages";
import type {
  DataSource,
  Dataset,
  DatasetColumn,
  Page,
  PageResult,
  RetInfo,
  Subject,
  Tag,
  TagMember,
  TagMemberStatus,
  TagReference
} from "./types";

/** Storage 的调用；读取带短时缓存，见 @/api/http。 */
const callStorage = <TReq extends object, TRsp>(method: string, req: TReq, options?: CallOptions) =>
  call<TReq, TRsp>("storage", method, req, options);

type RetRsp = { ret_info: RetInfo };

export async function upsertDataSource(data_source: DataSource, createOnly = false) {
  const rsp = await callStorage<{ data_source: DataSource; create_only?: boolean }, RetRsp & { data_source: DataSource }>(
    "UpsertDataSource",
    {
      data_source,
      ...(createOnly ? { create_only: true } : {})
    }
  );
  return rsp.data_source;
}

export function listDataSources(params: { space_id: string; keyword?: string; page?: Page }) {
  return callStorage<typeof params, RetRsp & { data_sources: DataSource[]; page_result: PageResult }>("ListDataSources", params);
}

export async function upsertSubject(subject: Subject) {
  const rsp = await callStorage<{ subject: Subject }, RetRsp & { subject: Subject }>("UpsertSubject", { subject });
  return rsp.subject;
}

/** 类型、市场等业务筛选不在 Storage 侧做；keyword 只匹配对象 ID 与名称。 */
export function listSubjects(params: { space_id: string; subject_ids?: string[]; keyword?: string; page?: Page }) {
  return callStorage<typeof params, RetRsp & { subjects: Subject[]; page_result: PageResult }>("ListSubjects", params);
}

export async function upsertTag(tag: Tag, createOnly = false) {
  const rsp = await callStorage<{ tag: Tag; create_only?: boolean }, RetRsp & { tag: Tag }>("UpsertTag", {
    tag,
    ...(createOnly ? { create_only: true } : {})
  });
  return rsp.tag;
}

export function listTags(spaceId: string, page: Page = { page: 1, size: 200 }) {
  return callStorage<{ space_id: string; page: Page }, RetRsp & { tags: Tag[]; page_result: PageResult }>("ListTags", {
    space_id: spaceId,
    page
  });
}

export function deleteTag(spaceId: string, tagId: string) {
  return callStorage<{ space_id: string; tag_id: string }, RetRsp & { references?: TagReference[] }>("DeleteTag", {
    space_id: spaceId,
    tag_id: tagId
  });
}

export function listTagMembers(params: { space_id: string; tag_id?: string; status?: string; keyword?: string; page: Page }) {
  return callStorage<typeof params, RetRsp & { members: TagMember[]; page_result: PageResult }>("ListTagMembers", params);
}

export function addTagMembers(spaceId: string, tagId: string, subjectIds: string[]) {
  return callStorage("AddTagMembers", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds });
}

export function removeTagMembers(spaceId: string, tagId: string, subjectIds: string[]) {
  return callStorage("RemoveTagMembers", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds });
}

export function setTagMemberStatus(spaceId: string, tagId: string, subjectIds: string[], status: TagMemberStatus) {
  return callStorage("SetTagMemberStatus", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds, status });
}

export function getDataset(params: { space_id: string; dataset_id: string }) {
  return callStorage<typeof params, RetRsp & { dataset: Dataset }>("GetDataset", params);
}

export function listDatasets(
  params: {
    space_id: string;
    data_source_id?: string;
    data_kind?: string;
    status?: "active" | "disabled";
    page?: Page;
  },
  options?: CallOptions
) {
  return callStorage<typeof params, RetRsp & { datasets: Dataset[]; page_result: PageResult }>("ListDatasets", params, options);
}

export function listDatasetColumns(params: { space_id: string; dataset_id: string; page?: Page }) {
  return callStorage<typeof params, RetRsp & { columns: DatasetColumn[]; page_result: PageResult }>("ListDatasetColumns", params);
}

/** 空间内全部数据集（翻完所有页）。 */
export function listAllDatasets(spaceId: string, options?: CallOptions) {
  return readAllPages(async page => {
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: 500 } }, options);
    return { items: rsp.datasets || [], hasMore: Boolean(rsp.page_result?.has_more) };
  });
}

/** 分页读取数据集的全部列：因子结果数据集会有几千列，单页 1000 列不够。 */
export function listAllDatasetColumns(spaceId: string, datasetId: string) {
  return readAllPages(async page => {
    const rsp = await listDatasetColumns({ space_id: spaceId, dataset_id: datasetId, page: { page, size: 1000 } });
    return { items: rsp.columns || [], hasMore: Boolean(rsp.page_result?.has_more) };
  }, 100);
}
