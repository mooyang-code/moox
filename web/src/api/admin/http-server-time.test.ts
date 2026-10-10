import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { recordServerDate, serverNow } from "./http";

vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

describe("server time from the Date header", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-09T08:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("follows the server clock when it is ahead or behind the browser", () => {
    recordServerDate("Fri, 09 Oct 2026 08:01:00 GMT");
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:01:00Z"));
    vi.advanceTimersByTime(10_000);
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:01:10Z"));
    recordServerDate("Fri, 09 Oct 2026 07:50:10 GMT");
    expect(serverNow()).toBe(Date.parse("2026-10-09T07:50:10Z"));
  });

  it("keeps the last offset when the header is missing or unreadable", () => {
    recordServerDate("Fri, 09 Oct 2026 08:05:00 GMT");
    recordServerDate(undefined);
    recordServerDate("not a date");
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:05:00Z"));
  });
});
