import { describe, expect, it } from "vitest";
import { SPACE_TIMEZONES, uncommonZoneHint } from "./space-zones";
import { resolveZone } from "./time-zone";

describe("空间常用时区", () => {
  it("都是合法的 IANA 名称，和安装种子一致", () => {
    expect(SPACE_TIMEZONES.map(item => item.value)).toEqual(["UTC", "Asia/Shanghai", "Asia/Hong_Kong", "America/New_York"]);
    for (const item of SPACE_TIMEZONES) expect(resolveZone(item.value)).toBe(item.value);
  });

  it("不在常用列表里的时区给出提示，没选时不提示", () => {
    expect(uncommonZoneHint("Europe/London")).not.toBe("");
    expect(uncommonZoneHint("UTC")).toBe("");
    expect(uncommonZoneHint("")).toBe("");
  });
});
