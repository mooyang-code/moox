let selectedSpaceIdCache = "";

// 空间代次：每次实际切换空间（含 A→B→A）递增。只比较空间 ID 无法识别“切走又切回”之后的旧请求。
let spaceGeneration = 0;

export function selectedSpaceGeneration(): number {
  return spaceGeneration;
}

export function setSelectedSpaceIdCache(spaceId: string): void {
  const next = spaceId || "";
  if (next !== readSelectedSpaceId()) spaceGeneration += 1;
  selectedSpaceIdCache = next;
}

export function readSelectedSpaceId(): string {
  if (selectedSpaceIdCache) return selectedSpaceIdCache;
  try {
    if (typeof localStorage === "undefined") return "";
    const raw = localStorage.getItem("spaceStore");
    if (!raw) return "";
    const parsed = JSON.parse(raw) as { selectedSpaceId?: string; state?: { selectedSpaceId?: string } };
    return parsed.selectedSpaceId || parsed.state?.selectedSpaceId || "";
  } catch {
    return "";
  }
}
