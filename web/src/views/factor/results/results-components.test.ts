import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FactorScope } from "./factor-scope";

const hoisted = vi.hoisted(() => ({ query: vi.fn(), warning: vi.fn() }));
vi.mock("@/api/storage/view", () => ({ queryTimeSeriesRows: hoisted.query }));
vi.mock("@arco-design/web-vue", () => ({ Message: { warning: hoisted.warning } }));

import FactorPickerModal from "./factor-picker-modal.vue";
import FactorValuesModal from "./factor-values-modal.vue";
import ColumnSettingsDrawer from "./column-settings-drawer.vue";

function scopeOf(count: number): FactorScope {
  const outputs = Array.from({ length: count }, (_, index) => ({
    column: `f${index}`,
    output: `f${index}`,
    alias: `m${Math.floor(index / 2)}`,
    factorId: `F${Math.floor(index / 2)}`,
    factorName: `F${Math.floor(index / 2)}`,
    categoryId: index % 2 ? "trend" : "momentum"
  }));
  const paramsByAlias = new Map(outputs.map(item => [item.alias, [{ column: `${item.alias}__window`, name: "window" }]]));
  return {
    outputs,
    outputByColumn: new Map(outputs.map(item => [item.column, item])),
    paramsByAlias,
    baseMarket: ["open", "close"],
    otherSource: ["request_id"],
    categoryNames: new Map([
      ["trend", "趋势类"],
      ["momentum", "动量类"]
    ]),
    categoryOrder: ["trend", "momentum"]
  };
}

// 只渲染内容与必要交互的 Arco 桩：表格按当前页渲染行，点行即选中。
const stubs = {
  "a-modal": defineComponent({
    props: ["visible", "okButtonProps"],
    emits: ["ok", "cancel"],
    template:
      '<div v-if="visible"><slot /><button class="ok" :disabled="okButtonProps?.disabled" @click="$emit(\'ok\')">确定</button></div>'
  }),
  "a-drawer": defineComponent({ props: ["visible"], template: '<div v-if="visible"><slot /></div>' }),
  "a-table": defineComponent({
    props: ["data", "pagination"],
    emits: ["row-click", "page-change"],
    template: `<div class="table"><button v-for="record in data.slice((pagination.current - 1) * pagination.pageSize, pagination.current * pagination.pageSize)"
      :key="record.column" class="row" @click="$emit('row-click', record)">{{ record.column }}</button>
      <button class="next" @click="$emit('page-change', pagination.current + 1)">下一页</button></div>`
  }),
  "a-input": defineComponent({
    props: ["modelValue", "placeholder"],
    emits: ["update:modelValue"],
    template:
      '<input :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
  }),
  "a-input-search": defineComponent({
    props: ["modelValue"],
    emits: ["update:modelValue", "input"],
    template:
      '<input class="search" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\')" />'
  }),
  "a-select": defineComponent({
    props: ["modelValue", "options"],
    emits: ["update:modelValue"],
    template:
      '<select class="op" :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>'
  }),
  "a-checkbox": defineComponent({
    props: ["modelValue", "disabled"],
    emits: ["change"],
    template:
      '<label><input type="checkbox" :checked="modelValue" :disabled="disabled" @change="$emit(\'change\', $event.target.checked)" /><slot /></label>'
  }),
  "a-button": defineComponent({ emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" }),
  "a-alert": { template: "<div class='alert'><slot /></div>" },
  "a-space": { template: "<div><slot /></div>" },
  "a-table-column": true,
  "icon-plus": true
};

describe("因子选择弹窗", () => {
  it("条件模式：选列、填数值后才能确定；非法数值和倒置区间不能确定", async () => {
    const wrapper = mount(FactorPickerModal, {
      props: { visible: true, mode: "condition", scope: scopeOf(3) },
      global: { stubs }
    });
    const ok = () => wrapper.get("button.ok");
    expect(ok().attributes("disabled")).toBeDefined();
    await wrapper.findAll("button.row")[1].trigger("click");
    await wrapper.get("input[placeholder='数值']").setValue("12abc");
    expect(ok().attributes("disabled")).toBeDefined();
    expect(wrapper.text()).toContain("请输入有效数字");

    await wrapper.get("select.op").setValue("range");
    await wrapper.get("input[placeholder='下限']").setValue("2");
    await wrapper.get("input[placeholder='上限']").setValue("1");
    expect(ok().attributes("disabled")).toBeDefined();
    expect(wrapper.text()).toContain("下限不能大于上限");
    await wrapper.get("input[placeholder='上限']").setValue("3");
    await ok().trigger("click");
    expect(wrapper.emitted("pick")?.[0]).toEqual([{ column: "f1", condition: { column: "f1", op: "range", min: 2, max: 3 } }]);
    expect(wrapper.emitted("update:visible")?.[0]).toEqual([false]);
  });

  it("按分类与关键词过滤，分页显示；钉住模式排除已钉住的列", async () => {
    const wrapper = mount(FactorPickerModal, {
      props: { visible: true, mode: "pin", scope: scopeOf(45), excluded: ["f0"] },
      global: { stubs }
    });
    expect(wrapper.findAll("button.row")).toHaveLength(20);
    expect(wrapper.findAll("button.row")[0].text()).toBe("f1");
    await wrapper.get("button.next").trigger("click");
    expect(wrapper.findAll("button.row")[0].text()).toBe("f21");

    await wrapper
      .findAll("li")
      .find(item => item.text().startsWith("趋势类"))!
      .trigger("click");
    expect(wrapper.findAll("button.row").every(row => Number(row.text().slice(1)) % 2 === 1)).toBe(true);
    await wrapper.get("input.search").setValue("f43");
    expect(wrapper.findAll("button.row").map(row => row.text())).toEqual(["f43"]);
    await wrapper.findAll("button.row")[0].trigger("click");
    await wrapper.get("button.ok").trigger("click");
    expect(wrapper.emitted("pick")?.[0]).toEqual([{ column: "f43" }]);
  });
});

describe("因子值弹窗", () => {
  const rowKey = {
    space_id: "crypto",
    dataset_id: "d",
    subject_id: "BTC",
    freq: "1m",
    series_tag: "",
    data_time: "2026-10-24T10:00:00Z"
  };

  beforeEach(() => {
    hoisted.query.mockReset();
  });

  it("每页单独查这一行：投影当前页输出与所属成员参数列，翻页再查", async () => {
    hoisted.query.mockResolvedValue({
      rows: [
        {
          key: rowKey,
          fields: [
            { field_id: "f0", value: { double_value: 0.123456789 } },
            { field_id: "m0__window", value: { int_value: 20 } }
          ]
        }
      ]
    });
    const wrapper = mount(FactorValuesModal, {
      props: { visible: true, viewId: "v", rowKey, scope: scopeOf(45) },
      global: { stubs }
    });
    await flushPromises();
    expect(hoisted.query).toHaveBeenCalledTimes(1);
    const first = hoisted.query.mock.calls[0][0];
    expect(first.selectors).toEqual([{ space_id: "crypto", dataset_id: "d", subject_id: "BTC", freq: "1m", series_tag: "" }]);
    expect(first.total_mode).toBe("NONE");
    expect(first.page).toEqual({ page: 1, size: 1 });
    expect(first.column_names.filter((name: string) => !name.includes("__"))).toHaveLength(20);
    expect(first.column_names).toContain("m0__window");

    await wrapper.get("button.next").trigger("click");
    await flushPromises();
    expect(hoisted.query).toHaveBeenCalledTimes(2);
    expect(hoisted.query.mock.calls[1][0].column_names[0]).toBe("f20");
  });

  it("过期的响应被丢弃", async () => {
    let resolveFirst!: (value: unknown) => void;
    hoisted.query
      .mockReturnValueOnce(new Promise(resolve => (resolveFirst = resolve)))
      .mockResolvedValueOnce({ rows: [{ key: rowKey, fields: [{ field_id: "f20", value: { double_value: 2 } }] }] });
    const wrapper = mount(FactorValuesModal, {
      props: { visible: true, viewId: "v", rowKey, scope: scopeOf(45) },
      global: { stubs }
    });
    await flushPromises();
    await wrapper.get("button.next").trigger("click");
    await flushPromises();
    resolveFirst({ rows: [{ key: rowKey, fields: [{ field_id: "f20", value: { double_value: 999 } }] }] });
    await flushPromises();
    expect((wrapper.vm as unknown as { values: Record<string, string> }).values).toEqual({ f20: "2" });
  });
});

describe("列设置抽屉", () => {
  const settings = { hiddenSystem: [], market: ["open", "close"], pinned: ["f0", "f1"] };

  function mountDrawer() {
    return mount(ColumnSettingsDrawer, {
      props: { visible: true, scope: scopeOf(4), settings },
      global: { stubs: { ...stubs, FactorPickerModal: true } }
    });
  }

  it("隐藏系统键与行情列、勾选其他源字段，行情列保持视图顺序", async () => {
    const wrapper = mountDrawer();
    const box = (text: string) =>
      wrapper
        .findAll("label")
        .find(item => item.text() === text)!
        .get("input");
    expect(box("数据ID").attributes("disabled")).toBeDefined();
    await box("频率").setValue(false);
    expect(wrapper.emitted("update:settings")?.[0]).toEqual([{ ...settings, hiddenSystem: ["freq"] }]);
    await box("request_id").setValue(true);
    expect(wrapper.emitted("update:settings")?.[1]).toEqual([{ ...settings, market: ["open", "close", "request_id"] }]);
    await box("开盘（open）").setValue(false);
    expect(wrapper.emitted("update:settings")?.[2]).toEqual([{ ...settings, market: ["close"] }]);
  });

  it("钉住列可以排序与移除", async () => {
    const wrapper = mountDrawer();
    await wrapper
      .findAll("button")
      .find(item => item.text() === "↓")!
      .trigger("click");
    expect(wrapper.emitted("update:settings")?.[0]).toEqual([{ ...settings, pinned: ["f1", "f0"] }]);
    await wrapper
      .findAll("button")
      .find(item => item.text() === "移除")!
      .trigger("click");
    expect(wrapper.emitted("update:settings")?.[1]).toEqual([{ ...settings, pinned: ["f1"] }]);
  });

  it("钉满上限时提示改用弹窗", async () => {
    const full = { ...settings, pinned: Array.from({ length: 30 }, (_, index) => `x${index}`) };
    const wrapper = mount(ColumnSettingsDrawer, {
      props: { visible: true, scope: scopeOf(4), settings: full },
      global: { stubs: { ...stubs, FactorPickerModal: true } }
    });
    await wrapper
      .findAll("button")
      .find(item => item.text().includes("添加外显因子列"))!
      .trigger("click");
    expect(hoisted.warning).toHaveBeenCalled();
  });
});
