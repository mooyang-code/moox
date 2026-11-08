import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DataSource } from "@/api/storage/types";

const api = vi.hoisted(() => ({
  listDataSources: vi.fn(async () => ({ data_sources: [], page_result: { total: 0 } })),
  createDataSource: vi.fn(async () => ({})),
  updateDataSource: vi.fn(async () => ({}))
}));
vi.mock("@/api/storage/metadata", () => api);
vi.mock("@/api/admin/spaces", () => ({
  listSpaces: vi.fn(async () => ({ spaces: [{ space_id: "crypto", name: "crypto", status: "active", timezone: "UTC" }] }))
}));
vi.mock("@/api/admin/http", () => ({ reportControlError: vi.fn() }));
import { useSpaceStore } from "@/store/modules/space";
import SourcesPage from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface PageVM {
  openCreate(): void;
  visible: boolean;
  form: DataSource;
}

let wrapper: VueWrapper;
beforeEach(() => {
  setActivePinia(createPinia());
  api.createDataSource.mockReset().mockResolvedValue({});
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

async function openCreateDialog() {
  const store = useSpaceStore();
  await store.loadSpaces();
  store.setSelectedSpace("crypto");
  wrapper = mount(SourcesPage, { global: { plugins: [ArcoVue] }, attachTo: document.body });
  await flushPromises();
  const vm = wrapper.vm as unknown as PageVM;
  vm.openCreate();
  await flushPromises();
  return vm;
}

/** 点弹窗的“确定”，走真实的 on-before-ok */
async function clickOk() {
  document.querySelector<HTMLElement>(".arco-modal-footer .arco-btn-primary")!.click();
  await flushPromises();
}

describe("数据源弹窗", () => {
  it("校验没过时点确定，弹窗不关、不发请求", async () => {
    const vm = await openCreateDialog();
    vm.form.name = "币安";
    await clickOk();
    expect(api.createDataSource).not.toHaveBeenCalled();
    expect(vm.visible).toBe(true);
    expect(vm.form.name).toBe("币安");
  });

  it("保存失败时弹窗不关，填的内容还在；成功后才关", async () => {
    const vm = await openCreateDialog();
    Object.assign(vm.form, { data_source_id: "binance", name: "币安" });
    api.createDataSource.mockRejectedValueOnce(new Error("服务不可用"));
    await clickOk();
    expect(api.createDataSource).toHaveBeenCalledTimes(1);
    expect(vm.visible).toBe(true);
    expect(vm.form.data_source_id).toBe("binance");

    await clickOk();
    expect(api.createDataSource).toHaveBeenCalledTimes(2);
    expect(vm.visible).toBe(false);
  });
});
