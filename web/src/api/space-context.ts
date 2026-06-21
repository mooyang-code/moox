export function withOptionalSpace<T extends Record<string, unknown>>(payload: T, spaceId?: string) {
  return spaceId ? { ...payload, space_id: spaceId } : payload;
}
