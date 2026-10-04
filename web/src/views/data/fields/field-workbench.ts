import type { FieldGroup } from "@/api/storage/types";

export interface FieldWorkbenchQuery {
  group: string;
  keyword: string;
  valueType: string;
  status: string;
  page: number;
  pageSize: number;
  sort: "sort_order" | "field_id" | "updated_at";
  order: "asc" | "desc";
}

export interface FieldGroupNode extends FieldGroup {
  children: FieldGroup[];
}

type RouteQuery = Record<string, unknown>;

const text = (value: unknown) => (Array.isArray(value) ? String(value[0] ?? "") : String(value ?? "")).trim();

function positiveInt(value: unknown, fallback: number) {
  const parsed = Number.parseInt(text(value), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export function fieldQueryFromRoute(query: RouteQuery): FieldWorkbenchQuery {
  const sort = text(query.sort);
  const order = text(query.order);
  const requestedSize = positiveInt(query.page_size, 20);
  return {
    group: text(query.group),
    keyword: text(query.keyword),
    valueType: text(query.value_type),
    status: text(query.status),
    page: positiveInt(query.page, 1),
    pageSize: [20, 50, 100].includes(requestedSize) ? requestedSize : 20,
    sort: sort === "field_id" || sort === "updated_at" ? sort : "sort_order",
    order: order === "desc" ? "desc" : "asc"
  };
}

export function fieldQueryToRoute(state: FieldWorkbenchQuery): Record<string, string> {
  const query: Record<string, string> = {};
  if (state.group) query.group = state.group;
  if (state.keyword) query.keyword = state.keyword.trim();
  if (state.valueType) query.value_type = state.valueType;
  if (state.status) query.status = state.status;
  if (state.page !== 1) query.page = String(state.page);
  if (state.pageSize !== 20) query.page_size = String(state.pageSize);
  if (state.sort !== "sort_order") query.sort = state.sort;
  if (state.order !== "asc") query.order = state.order;
  return query;
}

const byOrder = (a: FieldGroup, b: FieldGroup) =>
  (a.sort_order ?? 0) - (b.sort_order ?? 0) || a.group_id.localeCompare(b.group_id);

export function buildGroupTree(groups: FieldGroup[]): FieldGroupNode[] {
  return groups
    .filter(item => !item.parent_group_id)
    .sort(byOrder)
    .map(item => ({
      ...item,
      children: groups.filter(child => child.parent_group_id === item.group_id).sort(byOrder)
    }));
}

export function groupPath(groups: FieldGroup[], groupID: string) {
  const group = groups.find(item => item.group_id === groupID);
  if (!group) return groupID;
  if (!group.parent_group_id) return group.name;
  const parent = groups.find(item => item.group_id === group.parent_group_id);
  return parent ? `${parent.name} / ${group.name}` : group.name;
}

export function buildFieldGroupDeleteRequest(group: FieldGroup) {
  return {
    space_id: group.space_id.trim(),
    group_id: group.group_id.trim()
  };
}

export function isSpaceRequestCurrent(request: { space_id: string }, currentSpaceID: string) {
  return request.space_id !== "" && request.space_id === currentSpaceID.trim();
}

export { RequestGate } from "@/utils/request-gate";
