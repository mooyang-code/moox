let selectedSpaceIdCache = "";

// 空间代次：每次实际切换空间（含 A→B→A）递增。只比较空间 ID 无法识别“切走又切回”之后的旧请求。
let spaceGeneration = 0;

export function selectedSpaceGeneration(): number {
  return spaceGeneration;
}

const generationListeners = new Set<() => void>();

/** 空间实际切换（代次递增）时同步通知；返回取消订阅函数。 */
export function onSpaceGenerationChange(listener: () => void): () => void {
  generationListeners.add(listener);
  return () => generationListeners.delete(listener);
}

export function setSelectedSpaceIdCache(spaceId: string): void {
  const next = spaceId || "";
  const changed = next !== readSelectedSpaceId();
  if (changed) spaceGeneration += 1;
  selectedSpaceIdCache = next;
  if (changed) generationListeners.forEach(listener => listener());
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
