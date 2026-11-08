import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HoldersPreview } from "@/api/callback-dispatcher/delist";

const mocks = vi.hoisted(() => ({ listInstrumentHolders: vi.fn(), registerDelistNotice: vi.fn() }));
vi.mock("@/api/callback-dispatcher/delist", async () => ({
  ...(await vi.importActual<typeof import("@/api/callback-dispatcher/delist")>("@/api/callback-dispatcher/delist")),
  ...mocks
}));
vi.mock("@/api/admin/http", () => ({ reportControlError: vi.fn(), callControl: vi.fn() }));

import DelistDialog from "./delist-dialog.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const emptyPreview: HoldersPreview = { holders: [], verified: true, instrumentUnknown: false, skippedAccounts: [], notes: [] };
const FUTURE = Date.now() + 7 * 24 * 3600 * 1000;

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(r => (resolve = r));
  return { promise, resolve };
}

type DialogState = {
  form: { exchange_symbol: string; delist_at: number | string; paper_only: boolean };
  phase: string;
  previewing: boolean;
};

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});
beforeEach(() => {
  mocks.listInstrumentHolders.mockReset();
  mocks.registerDelistNotice.mockReset();
});

async function openDialog() {
  wrapper = mount(DelistDialog, {
    props: { visible: false, systemActionEnabled: true, systemRuleEnabled: true },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  await wrapper.setProps({ visible: true });
  await flushPromises();
  return wrapper;
}

const state = (page: VueWrapper) => page.vm as unknown as DialogState;
const button = (text: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".arco-modal button")].find(item => item.textContent?.trim() === text);

describe("手工登记下架对话框", () => {
  it("确认页和提交只用预览时的参数快照；预览期间表单禁用", async () => {
    const pending = deferred<HoldersPreview>();
    mocks.listInstrumentHolders.mockReturnValue(pending.promise);
    mocks.registerDelistNotice.mockResolvedValue({ created: true, event_id: "e1" });
    const page = await openDialog();
    Object.assign(state(page).form, { exchange_symbol: " fooUSDT ", delist_at: FUTURE, paper_only: true });
    await flushPromises();
    button("下一步")?.click();
    await flushPromises();
    expect(state(page).previewing).toBe(true);
    expect(document.querySelector<HTMLInputElement>(".arco-modal input")?.disabled).toBe(true);
    expect(mocks.listInstrumentHolders).toHaveBeenCalledWith(
      expect.objectContaining({ exchange_symbol: "FOOUSDT", paper_only: true })
    );

    // 预览期间又改了表单：确认页和提交都不受影响
    Object.assign(state(page).form, { exchange_symbol: "BARUSDT", paper_only: false });
    pending.resolve(emptyPreview);
    await flushPromises();
    expect(state(page).phase).toBe("confirm");
    const text = document.querySelector(".arco-modal")?.textContent ?? "";
    expect(text).toContain("FOOUSDT");
    expect(text).not.toContain("BARUSDT");
    expect(text).toContain("演练也会全局禁止买入这个标的");

    button("确认登记")?.click();
    await flushPromises();
    expect(mocks.registerDelistNotice).toHaveBeenCalledWith({
      exchange: "binance",
      market: "SPOT",
      exchange_symbol: "FOOUSDT",
      delist_at_ms: FUTURE,
      paper_only: true
    });
  });

  it("关掉重开后，上一次迟到的预览响应被丢弃，下一步按钮也不再转圈", async () => {
    const stale = deferred<HoldersPreview>();
    mocks.listInstrumentHolders.mockReturnValueOnce(stale.promise);
    const page = await openDialog();
    Object.assign(state(page).form, { exchange_symbol: "FOOUSDT", delist_at: FUTURE });
    button("下一步")?.click();
    await flushPromises();
    expect(state(page).previewing).toBe(true);

    await page.setProps({ visible: false });
    await page.setProps({ visible: true });
    await flushPromises();
    expect(state(page).previewing).toBe(false);
    stale.resolve({ ...emptyPreview, holders: [] });
    await flushPromises();
    expect(state(page).phase).toBe("form");
    expect(state(page).form.exchange_symbol).toBe("");
  });

  it("下架时间已经过去：确认页说明仍会写下架名单，真实下架结束后名单保留", async () => {
    mocks.listInstrumentHolders.mockResolvedValue(emptyPreview);
    const page = await openDialog();
    Object.assign(state(page).form, { exchange_symbol: "FOOUSDT", delist_at: Date.now() - 3600 * 1000 });
    button("下一步")?.click();
    await flushPromises();
    const text = document.querySelector(".arco-modal")?.textContent ?? "";
    expect(text).toContain("仍会写下架名单");
    expect(text).toContain("名单保留、不会解除");
  });
});
