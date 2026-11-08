import { createPinia, setActivePinia } from "pinia";
import { describe, expect, it } from "vitest";
import { useSpaceStore } from "@/store/modules/space";
import { useDisplayZone } from "./useDisplayZone";

describe("页面的显示时区", () => {
  it("跟空间有关的页面取当前空间登记的时区，切空间后跟着变；空或非法时 UTC", () => {
    setActivePinia(createPinia());
    const store = useSpaceStore();
    store.spaces = [
      { space_id: "crypto", name: "加密货币", status: "active", timezone: "UTC" },
      { space_id: "stockcn", name: "A股", status: "active", timezone: "Asia/Shanghai" },
      { space_id: "x", name: "未填", status: "active", timezone: "" }
    ];
    store.selectedSpaceId = "stockcn";
    const zone = useDisplayZone();
    expect(zone.value).toBe("Asia/Shanghai");
    store.selectedSpaceId = "crypto";
    expect(zone.value).toBe("UTC");
    store.selectedSpaceId = "x";
    expect(zone.value).toBe("UTC");
  });

  it("和空间无关的页面固定 UTC", () => {
    setActivePinia(createPinia());
    const store = useSpaceStore();
    store.spaces = [{ space_id: "stockcn", name: "A股", status: "active", timezone: "Asia/Shanghai" }];
    store.selectedSpaceId = "stockcn";
    expect(useDisplayZone("global").value).toBe("UTC");
  });
});
