import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

const source = readFileSync(resolve(process.cwd(), "src/views/strategy/components/strategy-instance-create.vue"), "utf8");

describe("strategy instance creation contract", () => {
  it("binds a definition, one factor task and an optional account, and stays disabled", () => {
    expect(source).toContain("createInstance({");
    expect(source).toContain("factor_task_id: form.factor_task_id");
    expect(source).toContain('Message.success("策略实例已创建并保持停用")');
    expect(source).not.toContain("setInstanceEnabled(");
    expect(source).not.toContain("input_bindings_json");
    expect(source).not.toContain("buildInputBindings");
  });

  it("prechecks the binding through the server instead of rebuilding it locally", () => {
    expect(source).toContain("validateStrategy(selectedStrategy.value.dsl_yaml, form.factor_task_id)");
    expect(source).toContain("ResolvedTable");
    expect(source).toContain("创建请求结果未知，但实例 ID 已存在");
  });
});

describe("strategy instance creation behavior", () => {
  const api = vi.hoisted(() => ({
    createInstance: vi.fn(),
    getInstance: vi.fn(),
    validateStrategy: vi.fn(),
    listFactorTasks: vi.fn(),
    listLogicalAccounts: vi.fn()
  }));
  vi.mock("@/api/strategy", () => ({
    createInstance: api.createInstance,
    getInstance: api.getInstance,
    validateStrategy: api.validateStrategy
  }));
  vi.mock("@/api/factor", () => ({ listFactorTasks: api.listFactorTasks }));
  vi.mock("@/api/trade", () => ({ listLogicalAccounts: api.listLogicalAccounts }));
  vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), info: vi.fn(), error: vi.fn() } }));

  const stubs = {
    "a-drawer": { props: ["visible"], template: '<div v-if="visible"><slot /><slot name="footer" /></div>' },
    "a-alert": { template: '<div class="alert"><slot /></div>' },
    "a-form": { template: "<form><slot /></form>" },
    "a-form-item": {
      props: ["help"],
      template: '<div class="item"><slot /><span class="help">{{ help }}</span><slot name="extra" /></div>'
    },
    "a-select": {
      props: ["modelValue"],
      emits: ["update:modelValue", "change"],
      template:
        '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value); $emit(\'change\')"><option value="" /><slot /></select>'
    },
    "a-option": { props: ["value"], template: '<option :value="value"><slot /></option>' },
    "a-radio-group": {
      name: "RadioGroupStub",
      props: ["modelValue"],
      emits: ["update:modelValue", "change"],
      template: '<div class="radios"><slot /></div>'
    },
    "a-radio": true,
    "a-descriptions": true,
    "a-descriptions-item": true,
    "a-input": true,
    "a-link": { emits: ["click"], template: '<a class="link" @click="$emit(\'click\')"><slot /></a>' },
    "a-space": { template: "<div><slot /></div>" },
    "a-tag": { template: "<span><slot /></span>" },
    "a-button": { template: "<button><slot /></button>" },
    ResolvedTable: true
  };
  const strategies = [{ strategy_id: "s1", name: "动量", dsl_yaml: "name: demo", dsl_hash: "h", created_at: "", updated_at: "" }];
  const taskInfo = (patch: Record<string, unknown> = {}, taskId = "ftask_a") => ({
    factor_task: { task_id: taskId, name: "现货因子", space_id: "crypto", freq: "1h", status: "enabled", ...patch },
    members: [
      { member_key: "ma", kind: "fixed", output_columns: ["ma"] },
      { member_key: "bias__var", kind: "var", output_columns: ["bias__var"] }
    ]
  });

  async function open(overrides: Record<string, unknown> = {}) {
    const { flushPromises, mount } = await import("@vue/test-utils");
    const Create = (await import("./strategy-instance-create.vue")).default;
    const wrapper = mount(Create, {
      props: { visible: false, strategies, spaceId: "crypto", ...overrides } as never,
      global: { stubs }
    });
    await wrapper.setProps({ visible: true });
    await flushPromises();
    return { wrapper, flushPromises };
  }

  beforeEach(() => {
    api.createInstance.mockReset().mockResolvedValue({ instance_id: "i1" });
    api.listFactorTasks.mockReset().mockResolvedValue({ factor_tasks: [taskInfo()], page_result: { has_more: false } });
    api.listLogicalAccounts.mockReset().mockResolvedValue({ logical_accounts: [], page_result: { has_more: false } });
  });

  it("preselects the preset definition", async () => {
    const { wrapper } = await open({ presetStrategyId: "s1" });
    expect((wrapper.find("select").element as HTMLSelectElement).value).toBe("s1");
  });

  it("still creates a compute-only instance when the account list fails", async () => {
    api.listLogicalAccounts.mockRejectedValue(new Error("账户服务不可用"));
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    const selects = wrapper.findAll("select");
    await selects[1].setValue("ftask_a");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_task_id: "ftask_a",
      logical_account_id: ""
    });
  });

  it("shows the account failure with a retry in trade mode and blocks submission without an account", async () => {
    api.listLogicalAccounts.mockRejectedValueOnce(new Error("账户服务不可用")).mockResolvedValueOnce({
      logical_accounts: [{ logical_account_id: "acc1", name: "账户一", space_id: "crypto" }],
      page_result: { has_more: false }
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    wrapper.findComponent({ name: "RadioGroupStub" }).vm.$emit("update:modelValue", "trade");
    await flushPromises();
    expect(wrapper.text()).toContain("账户列表读取失败：账户服务不可用");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("请选择组合账户");
    expect(api.createInstance).not.toHaveBeenCalled();
    // 重试后账户可选，提交时携带所选账户。
    await wrapper
      .findAll(".link")
      .find(item => item.text() === "重试")!
      .trigger("click");
    await flushPromises();
    await wrapper.findAll("select")[2].setValue("acc1");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_task_id: "ftask_a",
      logical_account_id: "acc1"
    });
  });

  it("offers a retry when the factor task list fails", async () => {
    api.listFactorTasks
      .mockRejectedValueOnce(new Error("因子服务不可用"))
      .mockResolvedValueOnce({ factor_tasks: [taskInfo()], page_result: { has_more: false } });
    const { wrapper, flushPromises } = await open();
    expect(wrapper.text()).toContain("因子任务列表读取失败：因子服务不可用");
    await wrapper.find(".link").trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("因子任务列表读取失败");
    expect(wrapper.findAll("option").some(item => item.text().includes("ftask_a"))).toBe(true);
  });

  it("offers only running, unbound tasks of the space and says so when there are none", async () => {
    api.listFactorTasks.mockResolvedValue({
      factor_tasks: [
        taskInfo({}, "ftask_ok"),
        taskInfo({ bound_instance_id: "i0" }, "ftask_bound"),
        taskInfo({ status: "disabled" }, "ftask_off"),
        taskInfo({ space_id: "stock" }, "ftask_other")
      ],
      page_result: { has_more: false }
    });
    const { wrapper } = await open({ presetStrategyId: "s1" });
    const options = wrapper.findAll("option").map(item => item.text());
    expect(options.some(text => text.includes("ftask_ok"))).toBe(true);
    for (const hidden of ["ftask_bound", "ftask_off", "ftask_other"])
      expect(options.some(text => text.includes(hidden))).toBe(false);

    api.listFactorTasks.mockResolvedValue({
      factor_tasks: [taskInfo({ bound_instance_id: "i0" })],
      page_result: { has_more: false }
    });
    const empty = await open({ presetStrategyId: "s1" });
    expect(empty.wrapper.text()).toContain("没有可用的因子任务");
  });

  it("lists the columns of the chosen task and the variable members the strategy must parameterise", async () => {
    const { wrapper } = await open({ presetStrategyId: "s1" });
    expect(wrapper.text()).not.toContain("任务的列");
    await wrapper.findAll("select")[1].setValue("ftask_a");
    expect(wrapper.text()).toContain("任务的列");
    expect(wrapper.text()).toContain("bias__var");
    expect(wrapper.text()).toContain("可变成员 bias__var");
  });
  it("initializes the form and metadata when it is mounted already open (deep link)", async () => {
    const { flushPromises, mount } = await import("@vue/test-utils");
    const Create = (await import("./strategy-instance-create.vue")).default;
    const wrapper = mount(Create, {
      props: { visible: true, strategies, spaceId: "crypto", presetStrategyId: "s1" } as never,
      global: { stubs }
    });
    await flushPromises();
    expect(api.listFactorTasks).toHaveBeenCalledTimes(1);
    expect((wrapper.find("select").element as HTMLSelectElement).value).toBe("s1");
  });

  it("emits created without asking the parent to close, so the parent's navigation wins", async () => {
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(wrapper.emitted("created")).toEqual([["i1"]]);
    expect(wrapper.emitted("update:visible")).toBeUndefined();
  });
});
