import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TradingAccount } from "@/api/trade";

vi.mock("./exchange-secrets", () => ({ loadExchangeSecrets: vi.fn(async () => [{ secretId: "s1", name: "密钥一" }]) }));
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
