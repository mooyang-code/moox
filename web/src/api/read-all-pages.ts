/** 一页的读取结果：这一页的条目，以及后面是否还有（has_more 或按总数判断，由调用方换算）。 */
export interface PageSlice<T> {
  items: T[];
  hasMore: boolean;
}

/**
 * 从第 1 页起翻完所有页，返回全部条目。空页或没有下一页时停止；超过 maxPages 页仍没读完时报错，不悄悄截断。
 */
export async function readAllPages<T>(fetchPage: (page: number) => Promise<PageSlice<T>>, maxPages = 50): Promise<T[]> {
  const items: T[] = [];
  for (let page = 1; page <= maxPages; page += 1) {
    const slice = await fetchPage(page);
    items.push(...slice.items);
    if (!slice.hasMore || !slice.items.length) return items;
  }
  throw new Error(`分页超过 ${maxPages} 页仍未读完，请缩小范围`);
}
