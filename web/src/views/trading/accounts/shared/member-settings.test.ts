import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TradingAccount } from "@/api/trade";

vi.mock("./exchange-secrets", () => ({
  loadExchangeSecrets: vi.fn(async () => [
    { secretId: "s1", name: "密钥一" },
    { secretId: "s2", name: "密钥二" }
  ])
}));
const api = vi.hoisted(() => ({ updateTradingAccount: vi.fn(async () => ({})), setLeverage: vi.fn(async () => ({})) }));
vi.mock("@/api/trade", async () => ({ ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")), ...api }));
import MemberSettings from "./member-settings.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const paperAccount = {
  trading_account_id: "p1",
  name: "币安模拟",
  exchange: 1,
  markets: [1, 2],
  execution_mode: 1,
  settlement_asset: "USDT",
  leverage_settings: { "*": "1" },
  paper: { initial_balance: "1000", maker_fee_rate: "0.0002", taker_fee_rate: "0.0005", slippage_bps: "1" }
} as unknown as TradingAccount;
const liveAccount = {
  trading_account_id: "l1",
  name: "测试网账户",
  exchange: 1,
  markets: [2],
  execution_mode: 2,
  settlement_asset: "USDT",
  leverage_settings: { "*": "3" },
  live: { environment: 1, credential_secret_id: "s1" }
} as unknown as TradingAccount;

afterEach(() => {
  document.body.innerHTML = "";
});

const mountIt = (account: TradingAccount) => mount(MemberSettings, { props: { account }, global: { plugins: [ArcoVue] } });

describe("执行账户设置", () => {
  it("模拟账户的名称、参数、杠杆只读展示，没有保存和设杠杆的控件", async () => {
    const wrapper = mountIt(paperAccount);
    await flushPromises();
    expect(wrapper.text()).toContain("币安模拟");
    expect(wrapper.text()).toContain("初始资金");
    expect(wrapper.text()).toContain("创建后都不能改");
    expect(wrapper.find("input").exists()).toBe(false);
    expect(wrapper.findAll("button").map(item => item.text())).not.toContain("保存");
    expect(wrapper.findAll("button").map(item => item.text())).not.toContain("设置");
    expect(wrapper.text()).toContain("1×");
    expect(wrapper.text()).not.toContain("检测");
  });

  it("实盘账户可以改名、选密钥和设杠杆", () => {
    const wrapper = mountIt(liveAccount);
    expect(wrapper.find("input").exists()).toBe(true);
    expect(wrapper.findAll("button").map(item => item.text())).toEqual(expect.arrayContaining(["保存", "设置"]));
  });

  it("详情页轮询整组换账户对象时，正在改的名称不被冲掉；服务端的值真的变了才回填", async () => {
    const wrapper = mountIt(liveAccount);
    const input = wrapper.find("input");
    await input.setValue("改了一半");
    await wrapper.setProps({ account: { ...liveAccount, live: { ...liveAccount.live! }, last_sync_at: "2" } as TradingAccount });
    expect((wrapper.find("input").element as HTMLInputElement).value).toBe("改了一半");
    await wrapper.setProps({ account: { ...liveAccount, name: "服务端改名" } as TradingAccount });
    expect((wrapper.find("input").element as HTMLInputElement).value).toBe("服务端改名");
  });
});

interface SettingsVM {
  name: string;
  secretId: string;
  leverageInstrument: string;
  leverageValue: string;
  confirmVisible: boolean;
  save(): void;
  saveLeverage(): void;
}

const prodAccount = {
  ...liveAccount,
  trading_account_id: "prod-1",
  name: "生产主账户",
  leverage_settings: { "*": "3", "BTC-USDT-SWAP": "3" },
  live: { environment: 2, credential_secret_id: "s1" }
} as unknown as TradingAccount;

/** 确认框里的按钮和名称输入框（ConfirmDialog 渲染在 body 下）。 */
const confirmButton = (text: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".tx-confirm button")].find(item => item.textContent?.trim() === text);
async function typeName(value: string) {
  const input = document.querySelector<HTMLInputElement>(".tx-confirm input");
  input!.value = value;
  input!.dispatchEvent(new Event("input"));
  await flushPromises();
}

describe("测试网、生产环境下的设置要先确认", () => {
  const mountLive = (account: TradingAccount) =>
    mount(MemberSettings, { props: { account }, global: { plugins: [ArcoVue] }, attachTo: document.body });

  afterEach(() => {
    api.updateTradingAccount.mockClear();
    api.setLeverage.mockClear();
  });

  it("生产账户换密钥：输入执行账户名称之前不能确认，输入后才保存且不再重复弹错", async () => {
    const wrapper = mountLive(prodAccount);
    await flushPromises();
    const vm = wrapper.vm as unknown as SettingsVM;
    vm.secretId = "s2";
    vm.save();
    await flushPromises();
    expect(document.body.textContent).toContain("交易所密钥：密钥一 → 密钥二");
    expect(document.body.textContent).toContain("输入执行账户名称「生产主账户」确认");
    expect(confirmButton("更换")?.disabled).toBe(true);
    confirmButton("更换")?.click();
    await flushPromises();
    expect(api.updateTradingAccount).not.toHaveBeenCalled();
    await typeName("生产主账户");
    expect(confirmButton("更换")?.disabled).toBe(false);
    confirmButton("更换")?.click();
    await flushPromises();
    expect(api.updateTradingAccount).toHaveBeenCalledWith(
      expect.objectContaining({ trading_account_id: "prod-1", credential_secret_id: "s2" }),
      { silent: true }
    );
    wrapper.unmount();
  });

  it("确认框打开期间 5 秒轮询换了账户对象：已输入的名称还在，提交的仍是打开时的请求", async () => {
    const wrapper = mountLive(prodAccount);
    await flushPromises();
    const vm = wrapper.vm as unknown as SettingsVM;
    vm.secretId = "s2";
    vm.save();
    await flushPromises();
    await typeName("生产主账户");
    await wrapper.setProps({ account: { ...prodAccount, last_sync_at: "later" } as TradingAccount });
    await flushPromises();
    expect(document.querySelector<HTMLInputElement>(".tx-confirm input")?.value).toBe("生产主账户");
    confirmButton("更换")?.click();
    await flushPromises();
    expect(api.updateTradingAccount).toHaveBeenCalledWith(
      { trading_account_id: "prod-1", name: undefined, credential_secret_id: "s2" },
      { silent: true }
    );
    wrapper.unmount();
  });

  it("只改名称不用确认", async () => {
    const wrapper = mountLive(prodAccount);
    await flushPromises();
    const vm = wrapper.vm as unknown as SettingsVM;
    vm.name = "新名字";
    vm.save();
    await flushPromises();
    expect(vm.confirmVisible).toBe(false);
    expect(api.updateTradingAccount).toHaveBeenCalledWith(
      { trading_account_id: "prod-1", name: "新名字", credential_secret_id: undefined },
      { silent: false }
    );
    wrapper.unmount();
  });

  it("杠杆不是正整数时被拦下，不打开确认框", async () => {
    const wrapper = mountLive(liveAccount);
    const vm = wrapper.vm as unknown as SettingsVM;
    for (const value of ["0", "1.5", "-1", "abc"]) {
      vm.leverageInstrument = "BTC-USDT-SWAP";
      vm.leverageValue = value;
      vm.saveLeverage();
      await flushPromises();
      expect(vm.confirmVisible).toBe(false);
    }
    expect(api.setLeverage).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("测试网设杠杆：确认框写明下次开仓前生效，不用输入名称，点确认才发请求", async () => {
    const wrapper = mountLive(liveAccount);
    const vm = wrapper.vm as unknown as SettingsVM;
    vm.leverageInstrument = "eth-usdt-swap";
    vm.leverageValue = "5";
    vm.saveLeverage();
    await flushPromises();
    expect(api.setLeverage).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("ETH-USDT-SWAP：未设置 → 5×");
    expect(document.body.textContent).toContain("下次开仓前按新倍数设置到交易所");
    expect(document.querySelector(".tx-confirm input")).toBeNull();
    confirmButton("设置")?.click();
    await flushPromises();
    expect(api.setLeverage).toHaveBeenCalledWith(
      { trading_account_id: "l1", instrument_id: "ETH-USDT-SWAP", leverage: "5" },
      { silent: true }
    );
    wrapper.unmount();
  });

  it("两段写法的标的按后端规则补成永续，确认框显示的是真正要改的那个标的的当前倍数", async () => {
    const wrapper = mountLive(prodAccount);
    const vm = wrapper.vm as unknown as SettingsVM;
    vm.leverageInstrument = "btc-usdt";
    vm.leverageValue = "5";
    vm.saveLeverage();
    await flushPromises();
    expect(document.body.textContent).toContain("BTC-USDT-SWAP：3× → 5×");
    await typeName("生产主账户");
    confirmButton("设置")?.click();
    await flushPromises();
    expect(api.setLeverage).toHaveBeenCalledWith(
      { trading_account_id: "prod-1", instrument_id: "BTC-USDT-SWAP", leverage: "5" },
      { silent: true }
    );
    wrapper.unmount();
  });
});
