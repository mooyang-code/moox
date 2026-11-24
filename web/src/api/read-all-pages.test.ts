import { describe, expect, it, vi } from "vitest";
import { readAllPages } from "./read-all-pages";

describe("readAllPages", () => {
  it("翻到没有下一页或空页为止", async () => {
    const fetchPage = vi.fn(async (page: number) => ({ items: [page], hasMore: page < 3 }));
    await expect(readAllPages(fetchPage)).resolves.toEqual([1, 2, 3]);
    expect(fetchPage).toHaveBeenCalledTimes(3);
    await expect(readAllPages(async () => ({ items: [], hasMore: true }))).resolves.toEqual([]);
  });

  it("超过页数上限时报错，不悄悄截断", async () => {
    await expect(readAllPages(async page => ({ items: [page], hasMore: true }), 2)).rejects.toThrow("2 页");
  });
});
