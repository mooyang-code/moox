import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue, { Message } from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FactorDef, FactorTaskInfo } from "@/api/factor/types";

const api = vi.hoisted(() => ({
  addFactorToTask: vi.fn(),
  setFactorMemberStatus: vi.fn(),
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
      freq: "5m",
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
  api.setFactorMemberStatus.mockResolvedValue({});
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
  it("任务启用时默认勾上“立即启用”：每个因子先添加再启用", async () => {
    const vm = await open("enabled", "f1");
    expect(vm.enableAfterAdd).toBe(true);
    expect(vm.selected).toEqual(["f1"]);
    const order: string[] = [];
    api.addFactorToTask.mockImplementation(async (_task: string, factorId: string) => {
      order.push(`add ${factorId}`);
      return { member_key: factorId, kind: "fixed" };
    });
    api.setFactorMemberStatus.mockImplementation(async (_task: string, key: string) => order.push(`enable ${key}`));
    vm.selected = ["f1", "f2"];
    await vm.submit();
    expect(order).toEqual(["add f1", "enable f1", "add f2", "enable f2"]);
    expect(api.setFactorMemberStatus).toHaveBeenCalledWith("t1", "f1", "enabled");
  });

  it("启用部分失败时列出没能启用的成员", async () => {
    const warning = vi.spyOn(Message, "warning");
    api.setFactorMemberStatus.mockImplementation(async (_task: string, key: string) => {
      if (key === "f2") throw new Error("引擎不可用");
    });
    const vm = await open("enabled");
    vm.selected = ["f1", "f2"];
    await vm.submit();
    expect(api.addFactorToTask).toHaveBeenCalledTimes(2);
    expect(JSON.stringify(warning.mock.calls)).toContain("f2（引擎不可用）");
  });

  it("任务停用时不勾也不能勾“立即启用”，只添加", async () => {
    const vm = await open("disabled");
    expect(vm.enableAfterAdd).toBe(false);
    vm.enableAfterAdd = true; // 即使状态被改，任务停用时也不启用
    vm.selected = ["f1"];
    await vm.submit();
    expect(api.addFactorToTask).toHaveBeenCalledTimes(1);
    expect(api.setFactorMemberStatus).not.toHaveBeenCalled();
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
    expect(api.addFactorToTask).toHaveBeenCalledWith("t1", "f1", "fixed", undefined);
  });
});
