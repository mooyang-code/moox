import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue, { Message } from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FactorDef, FactorTaskInfo } from "@/api/factor/types";

const api = vi.hoisted(() => ({
  addFactorToTask: vi.fn(),
  listFactors: vi.fn(),
  listFactorCategories: vi.fn()
}));
vi.mock("@/api/factor", () => api);
vi.mock("@/api/storage/metadata", () => ({ listDatasetColumns: vi.fn(async () => ({ columns: [{ column_name: "close" }] })) }));
import AddFactorModal from "./add-factor-modal.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const def = (id: string, category = "momentum"): FactorDef =>
  ({
    factor_id: id,
    name: id,
    factor_type: "timeseries",
    spec_hash: "h",
    input_columns: ["close"],
    outputs: [id],
    category_id: category
  }) as FactorDef;

const info = (status: "enabled" | "disabled"): FactorTaskInfo =>
  ({
    factor_task: {
      task_id: "t1",
      space_id: "crypto",
      source_dataset_id: "ds",
      frequency: "5m",
      name: "任务",
      result_dataset_id: "r",
      status
    },
    members: []
  }) as FactorTaskInfo;

let wrapper: VueWrapper;
beforeEach(() => {
  setActivePinia(createPinia());
  for (const mock of Object.values(api)) mock.mockReset();
  api.listFactors.mockResolvedValue({ factors: [{ factor: def("f1") }, { factor: def("f2", "volume") }], page_result: {} });
  api.listFactorCategories.mockResolvedValue([
    { category_id: "momentum", name: "动量类" },
    { category_id: "volume", name: "成交量类" }
  ]);
  api.addFactorToTask.mockImplementation(async (_task: string, factorId: string) => ({ member_key: factorId, kind: "fixed" }));
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

async function open(status: "enabled" | "disabled", presetFactorId?: string) {
  wrapper = mount(AddFactorModal, {
    props: { visible: false, info: info(status), title: "任务", presetFactorId },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  await wrapper.setProps({ visible: true });
  await flushPromises();
  return wrapper.vm as unknown as {
    enableAfterAdd: boolean;
    selected: string[];
    categoryId?: string;
    rows: { factor: FactorDef }[];
    submit(): Promise<void>;
  };
}

describe("添加因子弹窗", () => {
  it("任务启用时默认勾上“立即启用”：单次 AddTaskMember 传入 status=enabled", async () => {
    const vm = await open("enabled", "f1");
    expect(vm.enableAfterAdd).toBe(true);
    expect(vm.selected).toEqual(["f1"]);
    vm.selected = ["f1", "f2"];
    await vm.submit();
    expect(api.addFactorToTask).toHaveBeenNthCalledWith(1, "t1", "f1", "fixed", undefined, "enabled");
    expect(api.addFactorToTask).toHaveBeenNthCalledWith(2, "t1", "f2", "fixed", undefined, "enabled");
  });

  it("批量添加部分失败时保留失败项并提示", async () => {
    const warning = vi.spyOn(Message, "warning");
    api.addFactorToTask.mockImplementation(async (_task: string, factorId: string) => {
      if (factorId === "f2") throw new Error("引擎不可用");
      return { member_key: factorId, kind: "fixed" };
    });
    const vm = await open("enabled");
    vm.selected = ["f1", "f2"];
    await vm.submit();
    expect(api.addFactorToTask).toHaveBeenCalledTimes(2);
    expect(vm.selected).toEqual(["f2"]);
    expect(JSON.stringify(warning.mock.calls)).toContain("1 个已添加，1 个失败");
  });

  it("任务停用时不勾也不能勾“立即启用”，调用时不传 enabled", async () => {
    const vm = await open("disabled");
    expect(vm.enableAfterAdd).toBe(false);
    vm.enableAfterAdd = true; // 即使状态被改，任务停用时也不启用
    vm.selected = ["f1"];
    await vm.submit();
    expect(api.addFactorToTask).toHaveBeenCalledTimes(1);
    expect(api.addFactorToTask).toHaveBeenCalledWith("t1", "f1", "fixed", undefined, undefined);
  });

  it("按分类筛选候选因子", async () => {
    const vm = await open("enabled");
    vm.categoryId = "volume";
    await flushPromises();
    expect(vm.rows.map(row => row.factor.factor_id)).toEqual(["f2"]);
  });

  it("已选的因子被筛选藏起来时仍能提交", async () => {
    const vm = await open("enabled", "f1");
    vm.categoryId = "volume";
    await flushPromises();
    expect((vm as unknown as { canSubmit: boolean }).canSubmit).toBe(true);
    await vm.submit();
    expect(api.addFactorToTask).toHaveBeenCalledWith("t1", "f1", "fixed", undefined, "enabled");
  });
});
