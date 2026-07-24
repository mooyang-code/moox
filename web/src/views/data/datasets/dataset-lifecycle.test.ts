import fs from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

const callStorage = vi.hoisted(() => vi.fn());

vi.mock("@/api/storage/http", () => ({
  callStorage
}));

import { activateDataset, checkDatasetActivation, rebindDatasetDataNode } from "@/api/storage/metadata";

const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

describe("Dataset lifecycle page contract", () => {
  beforeEach(() => {
    callStorage.mockReset();
    callStorage.mockResolvedValue({ ret_info: { code: 0, msg: "success" } });
  });

  it("requires an active DataNode and keep duration, then forces new datasets disabled", () => {
    expect(source).toContain('field="data_node_id"');
    expect(source).toContain('field="keep_duration"');
    expect(source).toContain("被 View 使用时，Dataset 保留时长必须不小于 View 保留时长；0 表示永久保存。");
    expect(source).toContain('return { ...common, data_node_id: form.data_node_id, status: "disabled" }');
    expect(source).toContain('const activeDataNodes = computed(() => dataNodes.value.filter(item => item.status === "active"))');
  });

  it("keeps binding identity and lifecycle fields out of generic edits", () => {
    expect(source).toContain('return form.status === "disabled" ? { ...common, status: "disabled" } : common');
    expect(source).toContain("binding_locked");
    expect(source).toContain("revision");
    expect(source).toContain("当前 DataNode");
    expect(source).toContain("绑定状态");
    expect(source).not.toContain('field="status"');
  });

  it("checks every activation before enabling confirmation and uses the returned revision", () => {
    expect(source).toContain("await runActivationCheck()");
    expect(source).toContain('v-for="item in activationCheck?.checks || []"');
    expect(source).toContain(':ok-button-props="{ disabled: !activationReady || activationLoading }"');
    expect(source).toContain("if (!activationDataset.value || !activationCheck.value?.ready) return");
    expect(source).toContain("expected_revision: activationCheck.value.dataset_revision");
  });

  it("only exposes rebind before activation, excludes the current node, and sends row revision", () => {
    expect(source).toContain('return dataset.status === "disabled" && !dataset.binding_locked');
    expect(source).toContain("item.node_id !== rebindDataset.value?.data_node_id");
    expect(source).toContain("expected_revision: rebindDataset.value.revision ?? 0");
    expect(source).toContain("绑定永久锁定");
  });

  it("selects a known cross-space deep link and consumes it after opening the existing drawer", () => {
    expect(source).toContain("spaceStore.hasSpace(requestedSpaceId)");
    expect(source).toContain("spaceStore.setSelectedSpace(requestedSpaceId)");
    expect(source).toContain("openManage(target)");
    expect(source).toContain("await router.replace({ query })");
    expect(source).toContain("delete query.space_id");
    expect(source).toContain("delete query.dataset_id");
  });

  it("describes the binding rules beside the title and keeps the icon focusable", () => {
    expect(source).toContain('aria-label="数据集绑定规则说明"');
    expect(source).toContain("必须绑定一个 DataNode");
    expect(source).toContain("首次激活后绑定永久锁定");
    expect(source).toContain("系统不做数据迁移");
  });

  it("uses the lifecycle RPC names and exact CAS payloads", async () => {
    await checkDatasetActivation({ space_id: "space-a", dataset_id: "dataset-a" });
    expect(callStorage).toHaveBeenLastCalledWith("CheckDatasetActivation", {
      space_id: "space-a",
      dataset_id: "dataset-a"
    });

    await activateDataset({ space_id: "space-a", dataset_id: "dataset-a", expected_revision: 7 });
    expect(callStorage).toHaveBeenLastCalledWith("ActivateDataset", {
      space_id: "space-a",
      dataset_id: "dataset-a",
      expected_revision: 7
    });

    await rebindDatasetDataNode({
      space_id: "space-a",
      dataset_id: "dataset-a",
      data_node_id: "node-b",
      expected_revision: 8
    });
    expect(callStorage).toHaveBeenLastCalledWith("RebindDatasetDataNode", {
      space_id: "space-a",
      dataset_id: "dataset-a",
      data_node_id: "node-b",
      expected_revision: 8
    });
  });
});
