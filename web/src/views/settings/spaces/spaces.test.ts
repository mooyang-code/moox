import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Space } from "@/api/admin/types";

const api = vi.hoisted(() => ({
  listSpaces: vi.fn(),
  createSpace: vi.fn(async () => ({})),
  updateSpace: vi.fn(async () => ({}))
}));
vi.mock("@/api/admin/spaces", () => api);
vi.mock("@/api/admin/http", () => ({ reportControlError: vi.fn() }));
import { useSpaceStore } from "@/store/modules/space";
import SpacesPage from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const space = (id: string): Space => ({ space_id: id, name: id, status: "active", market: "", timezone: "UTC" });

interface PageVM {
  openCreate(): void;
  openEdit(record: Space): void;
  submit(): Promise<boolean>;
  form: Space;
}

let wrapper: VueWrapper;
beforeEach(() => {
  setActivePinia(createPinia());
  api.listSpaces.mockReset().mockResolvedValue({ spaces: [space("crypto"), space("stockcn")] });
  api.createSpace.mockClear();
  api.updateSpace.mockClear();
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function mountPage() {
  const store = useSpaceStore();
  await store.loadSpaces();
  store.setSelectedSpace("crypto");
  wrapper = mount(SpacesPage, { global: { plugins: [ArcoVue] }, attachTo: document.body });
  await flushPromises();
  return { store, vm: wrapper.vm as unknown as PageVM };
}

describe("空间设置", () => {
  it("编辑别的空间后不会把当前空间切过去", async () => {
    const { store, vm } = await mountPage();
    vm.openEdit(space("stockcn"));
    await flushPromises();
    vm.form.description = "改个描述";
    expect(await vm.submit()).toBe(true);
    await flushPromises();
    expect(api.updateSpace).toHaveBeenCalled();
    expect(store.selectedSpaceId).toBe("crypto");
  });

  it("新建的空间保存后切过去", async () => {
    const { store, vm } = await mountPage();
    vm.openCreate();
    await flushPromises();
    Object.assign(vm.form, { space_id: "stockus", name: "美股", timezone: "America/New_York" });
    api.listSpaces.mockResolvedValue({ spaces: [space("crypto"), space("stockcn"), space("stockus")] });
    expect(await vm.submit()).toBe(true);
    await flushPromises();
    expect(store.selectedSpaceId).toBe("stockus");
  });

  it("没填空间 ID 或保存失败时弹窗不关", async () => {
    const { vm } = await mountPage();
    vm.openCreate();
    await flushPromises();
    expect(await vm.submit()).toBe(false);
    expect(api.createSpace).not.toHaveBeenCalled();
    Object.assign(vm.form, { space_id: "x", name: "x" });
    // 新建必须选时区，不再默认上海
    expect(await vm.submit()).toBe(false);
    expect(api.createSpace).not.toHaveBeenCalled();
    vm.form.timezone = "UTC";
    api.createSpace.mockRejectedValueOnce(new Error("名称重复"));
    expect(await vm.submit()).toBe(false);
  });

  it("当前空间那一行标出“当前”", async () => {
    await mountPage();
    expect(document.body.textContent).toContain("crypto（当前）");
  });

  it("编辑时区为空的空间并保存时不会被写成 Asia/Shanghai", async () => {
    const { vm } = await mountPage();
    vm.openEdit({ ...space("crypto"), timezone: "" });
    await flushPromises();
    expect(vm.form.timezone).toBe("");
    expect(await vm.submit()).toBe(false);
    expect(api.updateSpace).not.toHaveBeenCalled();
  });
});
