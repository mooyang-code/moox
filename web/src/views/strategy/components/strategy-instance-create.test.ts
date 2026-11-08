import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

// vi.mock 总会被提升到文件顶部执行，对整个文件生效，所以写在顶层
const api = vi.hoisted(() => ({
  createInstance: vi.fn(),
  getInstance: vi.fn(),
  getStrategy: vi.fn(),
  validateStrategy: vi.fn(),
  listFactorTasks: vi.fn(),
  listLogicalAccounts: vi.fn(),
  listTradingAccounts: vi.fn(async () => ({ accounts: [] })),
  listAllDatasets: vi.fn()
}));
vi.mock("@/api/strategy", () => ({
  createInstance: api.createInstance,
  getInstance: api.getInstance,
  getStrategy: api.getStrategy,
  validateStrategy: api.validateStrategy
}));
vi.mock("@/api/factor", () => ({ listFactorTasks: api.listFactorTasks }));
vi.mock("@/api/storage/metadata", () => ({ listAllDatasets: api.listAllDatasets }));
vi.mock("@/api/trade", () => ({ listLogicalAccounts: api.listLogicalAccounts, listTradingAccounts: api.listTradingAccounts }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), info: vi.fn(), error: vi.fn() } }));

const source = readFileSync(resolve(process.cwd(), "src/views/strategy/components/strategy-instance-create.vue"), "utf8");

describe("strategy instance creation contract", () => {
  it("binds a definition, one factor task and an optional account, and stays disabled", () => {
    expect(source).toContain("createInstance({");
    expect(source).toContain("factor_tasks: factorTasks()");
    expect(source).toContain('Message.success("策略实例已创建并保持停用")');
    expect(source).not.toContain("setInstanceEnabled(");
    expect(source).not.toContain("input_bindings_json");
    expect(source).not.toContain("buildInputBindings");
  });

  it("prechecks the binding through the server instead of rebuilding it locally", () => {
    expect(source).toContain("validateStrategy(dsl, factorTasks())");
    expect(source).toContain("ResolvedTable");
    expect(source).toContain("创建请求结果未知，但实例 ID 已存在");
  });
});

describe("strategy instance creation behavior", () => {
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
  const strategies = [
    {
      strategy_id: "s1",
      name: "动量",
      dsl_yaml: "name: demo\nmarket: {select: swap}\n",
      dsl_hash: "h",
      created_at: "",
      updated_at: ""
    }
  ];
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

  // 创建前要先通过绑定预检（服务端核对市场与任务是否对应）。
  async function precheck(wrapper: { findAll: (selector: string) => any[] }, flushPromises: () => Promise<void>) {
    await wrapper
      .findAll("button")
      .find(item => item.text() === "先预检")!
      .trigger("click");
    await flushPromises();
  }

  async function clickCreate(wrapper: { findAll: (selector: string) => any[] }, flushPromises: () => Promise<void>) {
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
  }

  beforeEach(() => {
    api.validateStrategy.mockReset().mockResolvedValue({
      diagnostics: [],
      resolved_json: '{"market":{"select":"swap","prefer":"swap"}}',
      trial: null,
      trial_items: []
    });
    api.createInstance.mockReset().mockResolvedValue({ instance_id: "i1" });
    api.getStrategy.mockReset().mockRejectedValue(new Error("策略不存在"));
    api.listFactorTasks.mockReset().mockResolvedValue({ factor_tasks: [taskInfo()], page_result: { has_more: false } });
    api.listLogicalAccounts.mockReset().mockResolvedValue({ logical_accounts: [], page_result: { has_more: false } });
    api.listAllDatasets.mockReset().mockResolvedValue([]);
  });

  // 运行模式的单选组是页面里最后一个单选组（前面可能还有“输入方式”）。
  const runModeGroup = (wrapper: { findAllComponents: (selector: { name: string }) => any[] }) =>
    wrapper.findAllComponents({ name: "RadioGroupStub" }).at(-1)!;

  it("选中的定义不在目录里时说明原因，不会点了没反应", async () => {
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s-new" });
    await (wrapper.vm as unknown as { check(): Promise<void> }).check();
    await flushPromises();
    expect(wrapper.text()).toContain("策略目录里没有这条定义");
    expect(api.validateStrategy).not.toHaveBeenCalled();
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
    await precheck(wrapper, flushPromises);
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_tasks: { swap: "ftask_a" },
      logical_account_id: ""
    });
  });

  it("没预检直接创建：先预检再创建", async () => {
    const order: string[] = [];
    api.validateStrategy.mockImplementation(async () => {
      order.push("validate");
      return { diagnostics: [], resolved_json: '{"market":{"select":"swap","prefer":"swap"}}', trial: null, trial_items: [] };
    });
    api.createInstance.mockImplementation(async () => {
      order.push("create");
      return { instance_id: "i1" };
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    await clickCreate(wrapper, flushPromises);
    expect(order).toEqual(["validate", "create"]);
    expect(wrapper.emitted("created")?.[0]).toEqual(["i1"]);
  });

  it("预检不通过：不创建，显示诊断", async () => {
    api.validateStrategy.mockResolvedValue({
      diagnostics: ["市场和任务对不上"],
      resolved_json: "",
      trial: null,
      trial_items: []
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    await clickCreate(wrapper, flushPromises);
    expect(api.createInstance).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("市场和任务对不上");
    expect(wrapper.text()).toContain("预检没有通过");
  });

  it("预检通过后改了任务：旧结果作废，再点创建会重新预检；预检进行中再点创建不重复发请求", async () => {
    api.listFactorTasks.mockResolvedValue({
      factor_tasks: [taskInfo(), taskInfo({}, "ftask_b")],
      page_result: { has_more: false }
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    await precheck(wrapper, flushPromises);
    expect(api.validateStrategy).toHaveBeenCalledTimes(1);
    await wrapper.findAll("select")[1].setValue("ftask_b");
    await flushPromises();

    let release!: (value: unknown) => void;
    api.validateStrategy.mockImplementationOnce(() => new Promise(resolve => (release = resolve)));
    const create = wrapper.findAll("button").find(item => item.text() === "创建停用实例")!;
    await create.trigger("click");
    await create.trigger("click");
    expect(api.validateStrategy).toHaveBeenCalledTimes(2);
    release({ diagnostics: [], resolved_json: '{"market":{"select":"swap","prefer":"swap"}}', trial: null, trial_items: [] });
    await flushPromises();
    expect(api.validateStrategy.mock.calls[1][1]).toEqual({ swap: "ftask_b" });
    expect(api.createInstance).toHaveBeenCalled();
  });

  it("shows the account failure with a retry in trade mode and blocks submission without an account", async () => {
    api.listLogicalAccounts.mockRejectedValueOnce(new Error("账户服务不可用")).mockResolvedValueOnce({
      logical_accounts: [{ logical_account_id: "acc1", name: "账户一", space_id: "crypto", control_mode: 1, markets: [2] }],
      page_result: { has_more: false }
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    runModeGroup(wrapper).vm.$emit("update:modelValue", "trade");
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
    await precheck(wrapper, flushPromises);
    await wrapper.findAll("select")[2].setValue("acc1");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_tasks: { swap: "ftask_a" },
      logical_account_id: "acc1"
    });
  });

  it("lists no account for A-share instances and says why", async () => {
    api.listLogicalAccounts.mockResolvedValue({
      logical_accounts: [{ logical_account_id: "acc1", name: "账户一", space_id: "crypto", control_mode: 1, markets: [1] }],
      page_result: { has_more: false }
    });
    api.validateStrategy.mockReset().mockResolvedValue({
      diagnostics: [],
      resolved_json: '{"market":{"select":"equity","prefer":"equity"}}',
      trial: null,
      trial_items: []
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "先预检")!
      .trigger("click");
    runModeGroup(wrapper).vm.$emit("update:modelValue", "trade");
    await flushPromises();
    expect(wrapper.text()).toContain("A 股暂无交易账户，请改为“仅计算”。");
    expect(
      wrapper
        .findAll("select")[2]
        .findAll("option")
        .map(item => item.attributes("value"))
    ).toEqual([""]);
  });

  it("asks for a spot and a swap task for a mixed-market strategy and keeps one task from being picked twice", async () => {
    api.listFactorTasks.mockResolvedValue({
      factor_tasks: [taskInfo({ name: "现货因子" }, "ftask_spot"), taskInfo({ name: "永续因子" }, "ftask_swap")],
      page_result: { has_more: false }
    });
    const mixed = [{ ...strategies[0], dsl_yaml: "name: demo\nmarket: {select: both, prefer: swap}\n" }];
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1", strategies: mixed });
    const selects = wrapper.findAll("select");
    // 定义、现货任务、永续任务三个选择框。
    expect(selects).toHaveLength(3);
    expect(wrapper.text()).toContain("现货、永续 的因子任务");
    await selects[1].setValue("ftask_spot");
    await flushPromises();
    const swapOptions = wrapper
      .findAll("select")[2]
      .findAll("option")
      .map(item => item.attributes("value"));
    expect(swapOptions).not.toContain("ftask_spot");
    // 只选了一个市场的任务不能创建。
    const create = () => wrapper.findAll("button").find(item => item.text() === "创建停用实例")!;
    await create().trigger("click");
    await flushPromises();
    expect(api.createInstance).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("请为现货、永续各选一个因子任务");
    await wrapper.findAll("select")[2].setValue("ftask_swap");
    await precheck(wrapper, flushPromises);
    await create().trigger("click");
    await flushPromises();
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_tasks: { spot: "ftask_spot", swap: "ftask_swap" },
      logical_account_id: ""
    });
  });

  it("双市场策略不能写 factor_params：带可变成员的任务不可选并写明原因", async () => {
    api.listFactorTasks.mockResolvedValue({
      factor_tasks: [
        taskInfo({ name: "现货因子" }, "ftask_spot"),
        {
          ...taskInfo({ name: "永续固定" }, "ftask_fixed"),
          members: [{ member_key: "ma", kind: "fixed", output_columns: ["ma"] }]
        }
      ],
      page_result: { has_more: false }
    });
    const mixed = [{ ...strategies[0], dsl_yaml: "name: demo\nmarket: {select: both, prefer: swap}\n" }];
    const { wrapper } = await open({ presetStrategyId: "s1", strategies: mixed });
    const spotOption = wrapper
      .findAll("select")[1]
      .findAll("option")
      .find(item => item.attributes("value") === "ftask_spot")!;
    expect(spotOption.attributes("disabled")).toBeDefined();
    expect(spotOption.text()).toContain("有可变成员，不可选");
    const fixedOption = wrapper
      .findAll("select")[1]
      .findAll("option")
      .find(item => item.attributes("value") === "ftask_fixed")!;
    expect(fixedOption.attributes("disabled")).toBeUndefined();
    expect(wrapper.text()).toContain("现货、永续双市场策略不支持 factor_params");
  });

  describe("统一数据集上的任务（方式 B）", () => {
    const mixed = [{ ...strategies[0], dsl_yaml: "name: demo\nmarket: {select: both, prefer: swap}\n" }];
    const unifiedTasks = () => {
      api.listFactorTasks.mockResolvedValue({
        factor_tasks: [
          taskInfo({ name: "统一因子", source_dataset_id: "ds_uni" }, "ftask_uni"),
          taskInfo({ name: "现货因子", source_dataset_id: "ds_spot" }, "ftask_spot"),
          taskInfo({ name: "永续因子", source_dataset_id: "ds_swap" }, "ftask_swap")
        ],
        page_result: { has_more: false }
      });
      api.listAllDatasets.mockResolvedValue([
        { dataset_id: "ds_uni", attributes: { market_type: "multi" } },
        { dataset_id: "ds_spot", attributes: { market_type: "spot" } },
        { dataset_id: "ds_swap", attributes: {} }
      ]);
    };
    const optionValues = (select: { findAll: (selector: string) => any[] }) =>
      select.findAll("option").map(item => item.attributes("value"));

    it("lets a strategy with market pick one task on a unified dataset and registers it as multi", async () => {
      unifiedTasks();
      const { wrapper, flushPromises } = await open({ presetStrategyId: "s1", strategies: mixed });
      // 分开的方式里只列单市场数据集上的任务。
      expect(optionValues(wrapper.findAll("select")[1])).not.toContain("ftask_uni");
      wrapper.findAllComponents({ name: "RadioGroupStub" })[0].vm.$emit("update:modelValue", "unified");
      await flushPromises();
      const selects = wrapper.findAll("select");
      // 定义、统一数据集任务两个选择框，只列统一数据集上的任务。
      expect(selects).toHaveLength(2);
      expect(optionValues(selects[1]).filter(Boolean)).toEqual(["ftask_uni"]);
      await selects[1].setValue("ftask_uni");
      await precheck(wrapper, flushPromises);
      await wrapper
        .findAll("button")
        .find(item => item.text() === "创建停用实例")!
        .trigger("click");
      await flushPromises();
      expect(api.validateStrategy).toHaveBeenCalledWith(mixed[0].dsl_yaml, { multi: "ftask_uni" });
      expect(api.createInstance).toHaveBeenCalledWith({
        instance_id: undefined,
        strategy_id: "s1",
        factor_tasks: { multi: "ftask_uni" },
        logical_account_id: ""
      });
    });

    it("does not offer unified datasets to a strategy without market", async () => {
      unifiedTasks();
      const noMarket = [{ ...strategies[0], dsl_yaml: "name: demo\n" }];
      const { wrapper } = await open({ presetStrategyId: "s1", strategies: noMarket });
      expect(wrapper.text()).not.toContain("统一数据集上的一个因子任务");
    });

    it("keeps listing every task and says so when the datasets cannot be read", async () => {
      unifiedTasks();
      api.listAllDatasets.mockRejectedValue(new Error("存储服务不可用"));
      const { wrapper } = await open({ presetStrategyId: "s1", strategies: mixed });
      expect(wrapper.text()).toContain("数据集信息读取失败（存储服务不可用）");
      expect(optionValues(wrapper.findAll("select")[1])).toContain("ftask_uni");
    });
  });

  it("prechecks a mixed-market strategy with both tasks", async () => {
    api.listFactorTasks.mockResolvedValue({
      factor_tasks: [taskInfo({}, "ftask_spot"), taskInfo({}, "ftask_swap")],
      page_result: { has_more: false }
    });
    api.validateStrategy.mockReset().mockResolvedValue({ diagnostics: [], resolved_json: "", trial: null, trial_items: [] });
    const mixed = [{ ...strategies[0], dsl_yaml: "name: demo\nmarket: {select: both, prefer: spot}\n" }];
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1", strategies: mixed });
    await wrapper.findAll("select")[1].setValue("ftask_spot");
    await wrapper.findAll("select")[2].setValue("ftask_swap");
    await wrapper
      .findAll("button")
      .find(item => item.text() === "先预检")!
      .trigger("click");
    await flushPromises();
    expect(api.validateStrategy).toHaveBeenCalledWith(mixed[0].dsl_yaml, { spot: "ftask_spot", swap: "ftask_swap" });
  });

  it("offers a retry when the factor task list fails", async () => {
    api.listFactorTasks
      .mockRejectedValueOnce(new Error("因子服务不可用"))
      .mockResolvedValueOnce({ factor_tasks: [taskInfo()], page_result: { has_more: false } });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
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
    await precheck(wrapper, flushPromises);
    await wrapper
      .findAll("button")
      .find(item => item.text() === "创建停用实例")!
      .trigger("click");
    await flushPromises();
    expect(wrapper.emitted("created")).toEqual([["i1"]]);
    expect(wrapper.emitted("update:visible")).toBeUndefined();
  });

  it("没写 market 的策略要先选市场；选了任务后点创建会先预检再创建", async () => {
    const undeclared = [{ ...strategies[0], dsl_yaml: "name: demo\n" }];
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1", strategies: undeclared });
    const create = () => wrapper.findAll("button").find(item => item.text() === "创建停用实例")!;
    // 没写 market：没有默认市场，先选市场。
    expect(wrapper.findAll("select")).toHaveLength(1);
    await create().trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("先选择任务的市场");
    wrapper.findComponent({ name: "RadioGroupStub" }).vm.$emit("update:modelValue", "swap");
    await flushPromises();
    await wrapper.findAll("select")[1].setValue("ftask_a");
    // 选了任务但没有预检：点创建会先预检，通过后创建。
    await create().trigger("click");
    await flushPromises();
    expect(api.validateStrategy).toHaveBeenCalledTimes(1);
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_tasks: { swap: "ftask_a" },
      logical_account_id: ""
    });
  });

  it("预检后账户列表收窄：已选的账户不在新列表里时清掉", async () => {
    api.listLogicalAccounts.mockResolvedValue({
      logical_accounts: [
        { logical_account_id: "acc_swap", name: "永续户", space_id: "crypto", control_mode: 1, markets: [2] },
        { logical_account_id: "acc_both", name: "双市场户", space_id: "crypto", control_mode: 1, markets: [1, 2] }
      ],
      page_result: { has_more: false }
    });
    api.validateStrategy.mockResolvedValue({
      diagnostics: [],
      resolved_json: '{"market":{"select":"both","prefer":"swap"}}',
      trial: null,
      trial_items: []
    });
    const { wrapper, flushPromises } = await open({ presetStrategyId: "s1" });
    await wrapper.findAll("select")[1].setValue("ftask_a");
    runModeGroup(wrapper).vm.$emit("update:modelValue", "trade");
    await flushPromises();
    const accountSelect = () => wrapper.findAll("select").at(-1)!;
    await accountSelect().setValue("acc_swap");
    await flushPromises();
    expect((accountSelect().element as HTMLSelectElement).value).toBe("acc_swap");
    await precheck(wrapper, flushPromises);
    expect(
      accountSelect()
        .findAll("option")
        .map(item => item.attributes("value"))
    ).toEqual(["", "acc_both"]);
    expect((accountSelect().element as HTMLSelectElement).value).toBe("");
  });

  describe("策略池实例", () => {
    const def = (id: string, dsl: string) => ({ ...strategies[0], strategy_id: id, name: id, dsl_yaml: dsl });
    const spotMember = def("s_spot", "name: m\nmarket: {select: spot}\n");
    const swapMember = def("s_swap", "name: m\nmarket: {select: swap, prefer: swap}\n");
    const mixedMember = def("s_mixed", "name: m\nmarket: {select: both, prefer: swap}\n");
    const noMarketMember = def("s_plain", "name: m\n");
    const pool = (...members: string[]) =>
      def(
        "pool",
        `name: p\nbar: 1h\nstrategy_pool:\n  members:\n${members.map((id, index) => `    - {id: m${index}, strategy: ${id}}\n`).join("")}  allocation: {method: fixed, ratios: {m0: 1}}\n`
      );
    const create = (wrapper: { findAll: (selector: string) => any[] }) =>
      wrapper.findAll("button").find(item => item.text() === "创建停用实例")!;
    const resolved = (select: string, prefer: string) =>
      api.validateStrategy.mockReset().mockResolvedValue({
        diagnostics: [],
        resolved_json: JSON.stringify({ market: { select, prefer } }),
        trial: null,
        trial_items: []
      });

    it.each([
      ["纯现货池", [spotMember, spotMember], ["spot"], ["spot", "spot"]],
      ["纯永续池", [swapMember], ["swap"], ["swap", "swap"]]
    ])("%s只要绑定一个对应市场的因子任务", async (_name, members, markets, resolvedMarket) => {
      resolved(resolvedMarket[0], resolvedMarket[1]);
      const list = [pool(...members.map(item => item.strategy_id)), spotMember, swapMember];
      const { wrapper, flushPromises } = await open({ presetStrategyId: "pool", strategies: list });
      // 定义选择框 + 一个任务选择框，不需要先选市场
      expect(wrapper.findAll("select")).toHaveLength(2);
      expect(wrapper.text()).toContain("策略池要绑定各成员所需市场的并集");
      expect(wrapper.text()).toContain(`${markets[0] === "spot" ? "现货" : "永续"} 的因子任务`);
      await wrapper.findAll("select")[1].setValue("ftask_a");
      await precheck(wrapper, flushPromises);
      await create(wrapper).trigger("click");
      await flushPromises();
      expect(api.createInstance).toHaveBeenCalledWith({
        instance_id: undefined,
        strategy_id: "pool",
        factor_tasks: { [markets[0]]: "ftask_a" },
        logical_account_id: ""
      });
    });

    it("现货加永续的池（含 select=both 的成员）要现货、永续各绑一个任务，提交的仍是 市场→任务", async () => {
      api.listFactorTasks.mockResolvedValue({
        factor_tasks: [taskInfo({}, "ftask_spot"), taskInfo({}, "ftask_swap")],
        page_result: { has_more: false }
      });
      resolved("both", "spot");
      const list = [pool("s_spot", "s_swap"), spotMember, swapMember];
      const { wrapper, flushPromises } = await open({ presetStrategyId: "pool", strategies: list });
      expect(wrapper.findAll("select")).toHaveLength(3);
      expect(wrapper.text()).toContain("现货、永续 的因子任务");
      await wrapper.findAll("select")[1].setValue("ftask_spot");
      await wrapper.findAll("select")[2].setValue("ftask_swap");
      await precheck(wrapper, flushPromises);
      expect(api.validateStrategy).toHaveBeenCalledWith(list[0].dsl_yaml, { spot: "ftask_spot", swap: "ftask_swap" });
      await create(wrapper).trigger("click");
      await flushPromises();
      expect(api.createInstance).toHaveBeenCalledWith({
        instance_id: undefined,
        strategy_id: "pool",
        factor_tasks: { spot: "ftask_spot", swap: "ftask_swap" },
        logical_account_id: ""
      });
      // 单个成员 select=both 同样涉及两个市场
      const only = await open({ presetStrategyId: "pool", strategies: [pool("s_mixed"), mixedMember] });
      expect(only.wrapper.findAll("select")).toHaveLength(3);
    });

    it("还没预检时账户就按成员市场的并集过滤：只列出可交易市场包含现货与永续的账户", async () => {
      api.listLogicalAccounts.mockResolvedValue({
        logical_accounts: [
          { logical_account_id: "acc_spot", name: "现货户", space_id: "crypto", control_mode: 1, markets: [1] },
          { logical_account_id: "acc_both", name: "双市场户", space_id: "crypto", control_mode: 1, markets: [1, 2] }
        ],
        page_result: { has_more: false }
      });
      const { wrapper, flushPromises } = await open({
        presetStrategyId: "pool",
        strategies: [pool("s_spot", "s_swap"), spotMember, swapMember]
      });
      runModeGroup(wrapper).vm.$emit("update:modelValue", "trade");
      await flushPromises();
      const accountSelect = wrapper.findAll("select")[3];
      expect(accountSelect.findAll("option").map(item => item.attributes("value"))).toEqual(["", "acc_both"]);
    });

    it("成员定义不在目录里时按需读取；读取成功后按它的市场解析", async () => {
      api.getStrategy.mockResolvedValue(spotMember);
      const { wrapper } = await open({ presetStrategyId: "pool", strategies: [pool("s_spot")] });
      expect(api.getStrategy).toHaveBeenCalledWith("s_spot");
      expect(wrapper.text()).not.toContain("暂不能创建");
      expect(wrapper.findAll("select")).toHaveLength(2);
    });

    it("成员定义缺失时给出中文提示并禁止提交", async () => {
      const { wrapper, flushPromises } = await open({ presetStrategyId: "pool", strategies: [pool("s_gone")] });
      expect(wrapper.text()).toContain("成员 m0 引用的策略 s_gone 不存在（定义已删除，或读取失败）");
      expect(wrapper.findAll("select")).toHaveLength(1);
      await create(wrapper).trigger("click");
      await flushPromises();
      expect(wrapper.text()).toContain("策略池的成员还不能用");
      expect(api.createInstance).not.toHaveBeenCalled();
      expect(api.validateStrategy).not.toHaveBeenCalled();
    });

    it("成员没写 market 时给出中文提示并禁止提交，其他成员没问题也不例外", async () => {
      const { wrapper, flushPromises } = await open({
        presetStrategyId: "pool",
        strategies: [pool("s_spot", "s_plain"), spotMember, noMarketMember]
      });
      expect(wrapper.text()).toContain("成员 m1 引用的策略 s_plain 没有显式写 market，池成员必须写 market");
      expect(wrapper.findAll("select")).toHaveLength(1);
      await create(wrapper).trigger("click");
      await flushPromises();
      expect(api.createInstance).not.toHaveBeenCalled();
    });

    it("池里嵌套了池时提示不能嵌套", async () => {
      const inner = pool("s_spot");
      const { wrapper } = await open({
        presetStrategyId: "outer",
        strategies: [{ ...pool("inner"), strategy_id: "outer" }, { ...inner, strategy_id: "inner" }, spotMember]
      });
      expect(wrapper.text()).toContain("本身是策略池，策略池不能嵌套");
    });
  });
});
