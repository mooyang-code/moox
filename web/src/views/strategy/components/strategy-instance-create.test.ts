import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

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
    expect(source).toContain("validateStrategy(selectedStrategy.value.dsl_yaml, factorTasks())");
    expect(source).toContain("ResolvedTable");
    expect(source).toContain("创建请求结果未知，但实例 ID 已存在");
  });
});

describe("strategy instance creation behavior", () => {
  const api = vi.hoisted(() => ({
    createInstance: vi.fn(),
    getInstance: vi.fn(),
    getStrategy: vi.fn(),
    validateStrategy: vi.fn(),
    listFactorTasks: vi.fn(),
    listLogicalAccounts: vi.fn()
  }));
  vi.mock("@/api/strategy", () => ({
    createInstance: api.createInstance,
    getInstance: api.getInstance,
    getStrategy: api.getStrategy,
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
      .find(item => item.text() === "解析列与成员契约")!
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

  it("shows the account failure with a retry in trade mode and blocks submission without an account", async () => {
    api.listLogicalAccounts.mockRejectedValueOnce(new Error("账户服务不可用")).mockResolvedValueOnce({
      logical_accounts: [{ logical_account_id: "acc1", name: "账户一", space_id: "crypto", control_mode: 1, markets: [2] }],
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
      .find(item => item.text() === "解析列与成员契约")!
      .trigger("click");
    wrapper.findComponent({ name: "RadioGroupStub" }).vm.$emit("update:modelValue", "trade");
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
      .find(item => item.text() === "解析列与成员契约")!
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

  it("refuses to create before the binding precheck passes and without choosing a market for an undeclared strategy", async () => {
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
    // 选了任务但没有预检：不能创建。
    await create().trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("请先点击“解析列与成员契约”");
    expect(api.createInstance).not.toHaveBeenCalled();
    await precheck(wrapper, flushPromises);
    await create().trigger("click");
    await flushPromises();
    expect(api.createInstance).toHaveBeenCalledWith({
      instance_id: undefined,
      strategy_id: "s1",
      factor_tasks: { swap: "ftask_a" },
      logical_account_id: ""
    });
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
      wrapper.findComponent({ name: "RadioGroupStub" }).vm.$emit("update:modelValue", "trade");
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
