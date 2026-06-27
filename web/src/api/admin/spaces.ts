import { callControl } from './http';
import type { PageReq, PageResult, Space, SpaceMember } from './types';

export interface ListSpacesReq {
  owner?: string;
  status?: string;
  page?: PageReq;
}

export interface ListSpacesRsp {
  spaces: Space[];
  page_result?: PageResult;
}

export function listSpaces(req: ListSpacesReq = {}) {
  return callControl<ListSpacesReq, ListSpacesRsp>('space', 'ListSpaces', req);
}

export function createSpace(space: Space) {
  return callControl<{ space: Space }, { space: Space }>('space', 'CreateSpace', { space });
}

export function updateSpace(space: Space) {
  return callControl<{ space: Space }, { space: Space }>('space', 'UpdateSpace', { space });
}

export function listSpaceMembers(req: { space_id: string; page?: PageReq }) {
  return callControl<typeof req, { members: SpaceMember[]; page_result?: PageResult }>('space', 'ListSpaceMembers', req);
}
