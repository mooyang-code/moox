import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ issue: vi.fn() }));
vi.mock("@/api/modules/user", () => ({ issueRawSessionTicketAPI: mocks.issue }));
import { ArchiveRawError, dispositionFileName, downloadArchivePartition, fetchArchiveRecentRows } from "./raw";

const fetchMock = vi.fn();
beforeEach(() => {
  let n = 0;
  mocks.issue.mockReset().mockImplementation(async () => ({ ticket: `t${++n}`, expires_at: 0 }));
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("归档裸 HTTP", () => {
  it("最新数据：每次都重新取 ticket，资源是 <空间>/<数据集>，参数都编码", async () => {
    fetchMock.mockImplementation(async () => json(200, { columns: [], rows: [], staged_until: "" }));
    await fetchArchiveRecentRows({ space: "crypto", dataset: "ds_1m", subject: "BTC/USDT&x", limit: 200 });
    await fetchArchiveRecentRows({
      space: "crypto",
      dataset: "ds_1m",
      subject: "BTC",
      before: "2026-11-07T00:00:00Z",
      limit: 200
    });
    expect(mocks.issue).toHaveBeenNthCalledWith(1, "archive_recent", "crypto/ds_1m");
    expect(mocks.issue).toHaveBeenCalledTimes(2);
    const [first, second] = fetchMock.mock.calls.map(call => new URL(String(call[0])));
    expect(first.pathname).toBe("/api/admin/archive/RecentRows");
    expect(first.searchParams.get("ticket")).toBe("t1");
    expect(first.searchParams.get("subject")).toBe("BTC/USDT&x");
    expect(first.searchParams.has("before")).toBe(false);
    expect(second.searchParams.get("ticket")).toBe("t2");
    expect(second.searchParams.get("before")).toBe("2026-11-07T00:00:00Z");
  });

  it("失败时抛出响应体里的 msg 和状态码；读不出 msg 时按状态码说明", async () => {
    fetchMock.mockResolvedValueOnce(
      json(409, { code: 409, msg: "远端副本未就绪：当前版本还没有上传或还没有校验", success: false })
    );
    await expect(fetchArchiveRecentRows({ space: "s", dataset: "d", subject: "x" })).rejects.toMatchObject({
      message: "远端副本未就绪：当前版本还没有上传或还没有校验",
      status: 409
    });
    fetchMock.mockResolvedValueOnce(new Response("<html>bad gateway</html>", { status: 502 }));
    await expect(fetchArchiveRecentRows({ space: "s", dataset: "d", subject: "x" })).rejects.toBeInstanceOf(ArchiveRawError);
    fetchMock.mockResolvedValueOnce(new Response("", { status: 429 }));
    await expect(fetchArchiveRecentRows({ space: "s", dataset: "d", subject: "x" })).rejects.toThrow("同时下载的数量已达上限");
  });

  it("下载：取 archive_download 的 ticket，文件名取响应头，报告进度", async () => {
    const body = new Uint8Array([1, 2, 3, 4]);
    fetchMock.mockResolvedValueOnce(
      new Response(body, {
        status: 200,
        headers: { "Content-Length": "4", "Content-Disposition": 'attachment; filename="binance-spot-1m_2026-11-07.csv.gz"' }
      })
    );
    const progress = vi.fn();
    const result = await downloadArchivePartition(
      { space: "crypto", dataset: "ds", partition: "2026-11-07", fallbackName: "fallback.csv.gz" },
      progress
    );
    expect(mocks.issue).toHaveBeenCalledWith("archive_download", "crypto/ds");
    const url = new URL(String(fetchMock.mock.calls[0][0]));
    expect(url.pathname).toBe("/api/admin/archive/PartitionDownload");
    expect(url.searchParams.get("partition")).toBe("2026-11-07");
    expect(result.fileName).toBe("binance-spot-1m_2026-11-07.csv.gz");
    expect(result.blob.size).toBe(4);
    expect(progress).toHaveBeenLastCalledWith(4, 4);
  });

  it("下载：长度不足按下载不完整报错，不交出残缺文件", async () => {
    fetchMock.mockResolvedValueOnce(new Response(new Uint8Array([1, 2]), { status: 200, headers: { "Content-Length": "10" } }));
    await expect(
      downloadArchivePartition({ space: "s", dataset: "d", partition: "2026-11-07", fallbackName: "f" })
    ).rejects.toThrow("下载不完整");
  });

  it("下载：读取中途抛错按下载不完整报错", async () => {
    let sent = false;
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        if (!sent) {
          sent = true;
          controller.enqueue(new Uint8Array([1, 2]));
        } else controller.error(new TypeError("network error"));
      }
    });
    fetchMock.mockResolvedValueOnce(new Response(body, { status: 200, headers: { "Content-Length": "10" } }));
    await expect(
      downloadArchivePartition({ space: "s", dataset: "d", partition: "2026-11-07", fallbackName: "f" })
    ).rejects.toThrow("下载不完整：连接中断，请重试");
  });

  it("401（ticket 已用过或过期）提示链接已失效；其他错误兼容 ret_info.msg", async () => {
    fetchMock.mockResolvedValueOnce(json(401, { ret_info: { code: 401, msg: "invalid raw route ticket" } }));
    await expect(fetchArchiveRecentRows({ space: "s", dataset: "d", subject: "x" })).rejects.toMatchObject({
      message: "链接已失效，请重试",
      status: 401
    });
    fetchMock.mockResolvedValueOnce(json(500, { ret_info: { code: 500, msg: "读取主机拓扑失败" } }));
    await expect(fetchArchiveRecentRows({ space: "s", dataset: "d", subject: "x" })).rejects.toThrow("读取主机拓扑失败");
  });

  it("Content-Disposition 取不到文件名时为空", () => {
    expect(dispositionFileName(null)).toBe("");
    expect(dispositionFileName("attachment")).toBe("");
  });
});
