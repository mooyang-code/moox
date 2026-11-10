import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h, provide, ref } from "vue";
import type { LogicalAccount } from "@/api/trade";
import type { FundSettings } from "@/api/trade/settings";
import type { Environment } from "@/components/trade-confirm/environment";

const mocks = vi.hoisted(() => ({ listLogicalAccounts: vi.fn(), updateFund: vi.fn(), updateOrder: vi.fn() }));
vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  listLogicalAccounts: mocks.listLogicalAccounts
}));
vi.mock("@/api/trade/settings", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade/settings")>("@/api/trade/settings")),
  updateLogicalAccountFundSettings: mocks.updateFund,
  updateLogicalAccountOrderSettings: mocks.updateOrder
}));
import { detailContextKey, type DetailContext } from "./context";
import ConfirmDialog from "@/components/trade-confirm/confirm-dialog.vue";
import FundSettingsForm from "../../settings/fund-settings-form.vue";
import SettingsTab from "./settings-tab.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

let wrapper: VueWrapper;
const reload = vi.fn(async () => undefined);
const saved: FundSettings = { reserve_ratio: "0", bnb_discount: false, bnb_reserve_amount: "0", dust_convert_enabled: false };

function mountTab(env: Environment, stored: Partial<FundSettings> = {}) {
  const logical = ref({
    logical_account_id: "l-1",
    name: "组合",
    control_mode: 2,
    execution_mode: env === "paper" ? 1 : 2,
    settlement_asset: "USDT",
    members: [],
    fund_settings: { ...saved, ...stored }
  } as unknown as LogicalAccount);
  wrapper = mount(
    defineComponent({
      setup() {
        provide(detailContextKey, {
          logical: computed(() => logical.value),
          accounts: computed(() => new Map()),
          allAccounts: ref([]),
          env: computed(() => env),
          strategy: ref(null),
          snapshot: ref({ swapPositions: [], spotHoldings: [] }),
          membersEditable: computed(() => true),
          reload,
          openClosePaper: vi.fn()
        } as unknown as DetailContext);
        return () => h(SettingsTab);
      }
    }),
    { global: { plugins: [ArcoVue] } }
  );
}

async function saveFund(settings: Partial<FundSettings>) {
  wrapper.findComponent(FundSettingsForm).vm.$emit("save", { ...saved, ...settings });
  await flushPromises();
}

// 只在弹出的确认框里找，不会误中页面上同名的表单项
const dialogTitle = () =>
  [...document.querySelectorAll(".arco-modal")].some(item => item.textContent?.includes("关闭保证金率监控"));
function dialogButton(text: string) {
  return [...document.querySelectorAll<HTMLButtonElement>(".arco-modal button")].find(item => item.textContent?.trim() === text);
}
const marginDialog = () => wrapper.findAllComponents(ConfirmDialog).find(item => item.props("title") === "关闭保证金率监控");

beforeEach(() => {
  mocks.listLogicalAccounts.mockReset().mockResolvedValue({ logical_accounts: [] });
  mocks.updateFund.mockReset().mockResolvedValue({});
  mocks.updateOrder.mockReset().mockResolvedValue({});
  reload.mockClear();
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("设置页保存资金设置", () => {
  it("把保证金率监控从开着改成关闭：先弹确认，取消不保存", async () => {
    mountTab("testnet", { margin_monitor_disabled: false });
    await saveFund({ margin_monitor_disabled: true });
    expect(dialogTitle()).toBe(true);
    expect(mocks.updateFund).not.toHaveBeenCalled();
    const cancel = dialogButton("取消");
    expect(cancel).toBeTruthy();
    cancel?.click();
    await flushPromises();
    expect(marginDialog()?.props("visible")).toBe(false);
    expect(mocks.updateFund).not.toHaveBeenCalled();
  });

  it("确认后只保存一次，带上整份设置", async () => {
    mountTab("testnet", { margin_monitor_disabled: false });
    await saveFund({ margin_monitor_disabled: true, reserve_ratio: "0.1" });
    dialogButton("关闭并保存")?.click();
    await flushPromises();
    expect(mocks.updateFund).toHaveBeenCalledTimes(1);
    expect(mocks.updateFund).toHaveBeenCalledWith(
      "l-1",
      expect.objectContaining({ margin_monitor_disabled: true, reserve_ratio: "0.1" })
    );
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("生产环境要先输入组合名称才能确认，输入后保存一次", async () => {
    mountTab("prod", { margin_monitor_disabled: false });
    await saveFund({ margin_monitor_disabled: true });
    expect(dialogTitle()).toBe(true);
    const ok = dialogButton("关闭并保存");
    expect(ok?.disabled).toBe(true);
    ok?.click();
    await flushPromises();
    expect(mocks.updateFund).not.toHaveBeenCalled();
    const input = document.querySelector<HTMLInputElement>(".arco-modal input");
    expect(input).toBeTruthy();
    input!.value = "组合";
    input!.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(dialogButton("关闭并保存")?.disabled).toBe(false);
    dialogButton("关闭并保存")?.click();
    await flushPromises();
    expect(mocks.updateFund).toHaveBeenCalledTimes(1);
    expect(marginDialog()?.props("visible")).toBe(false);
  });

  it("不涉及关闭监控（保持开着、本来就是关的）时直接保存，不弹确认", async () => {
    mountTab("testnet", { margin_monitor_disabled: false });
    await saveFund({ margin_monitor_disabled: false, reserve_ratio: "0.2" });
    expect(dialogTitle()).toBe(false);
    expect(mocks.updateFund).toHaveBeenCalledTimes(1);
    wrapper.unmount();
    document.body.innerHTML = "";
    mocks.updateFund.mockClear();
    mountTab("testnet", { margin_monitor_disabled: true });
    await saveFund({ margin_monitor_disabled: true });
    expect(dialogTitle()).toBe(false);
    expect(mocks.updateFund).toHaveBeenCalledTimes(1);
  });

  it("模拟盘不监控保证金率，不弹确认", async () => {
    mountTab("paper", { margin_monitor_disabled: false });
    await saveFund({ margin_monitor_disabled: true });
    expect(dialogTitle()).toBe(false);
    expect(mocks.updateFund).toHaveBeenCalledTimes(1);
  });
});
