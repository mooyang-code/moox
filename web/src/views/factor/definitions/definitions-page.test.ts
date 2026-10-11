import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listFactors: vi.fn(),
  spaceId: undefined as { value: string } | undefined
}));

vi.mock("@/api/factor", () => ({ listFactors: mocks.listFactors, deleteFactor: vi.fn() }));
vi.mock("@/views/factor/shared/use-factor-scope", async () => {
  const { ref: vueRef } = await import("vue");
  mocks.spaceId = vueRef("");
  return {
    useFactorScope: () => ({
      store: { sets: [], reload: vi.fn(), setLabel: (set: { set_id: string }) => set.set_id },
      spaceId: mocks.spaceId
    })
  };
});
vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));

import DefinitionsPage from "./index.vue";

describe("factor definitions page", () => {
  beforeEach(() => {
    mocks.listFactors.mockReset().mockResolvedValue({ factors: [], page_result: { has_more: false } });
    if (mocks.spaceId) mocks.spaceId.value = "";
  });

  it("waits for the selected space before listing and reloads when it appears", async () => {
    const wrapper = mount(DefinitionsPage, { global: { stubs: { FactorDetailDrawer: true, InfoTip: true } } });
    await flushPromises();
    expect(mocks.listFactors).not.toHaveBeenCalled();

    mocks.spaceId!.value = "crypto";
    await flushPromises();
    expect(mocks.listFactors).toHaveBeenCalledTimes(1);

    mocks.spaceId!.value = "equity";
    await flushPromises();
    expect(mocks.listFactors).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });
});
