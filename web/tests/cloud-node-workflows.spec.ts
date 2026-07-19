import { describe, expect, it } from "vitest";
import { useCloudNodeBatchAdd } from "@/views/collector/cloud-node/composables/use-cloud-node-batch-add";
import { useCloudNodeDeploy } from "@/views/collector/cloud-node/composables/use-cloud-node-deploy";
import { useCloudNodeList } from "@/views/collector/cloud-node/composables/use-cloud-node-list";

describe("cloud node workflows", () => {
  it("keeps selection scoped to the current node list", () => {
    const list = useCloudNodeList<{ node_id: string }>();
    list.replaceRows([{ node_id: "node-a" }, { node_id: "node-b" }]);
    list.toggleSelection(["node-a", "missing"]);
    expect(list.selectedKeys.value).toEqual(["node-a", "missing"]);
    list.replaceRows([{ node_id: "node-a" }]);
    expect(list.selectedKeys.value).toEqual(["node-a"]);
  });

  it("only enables a complete batch plan", () => {
    const batch = useCloudNodeBatchAdd();
    batch.open(2);
    batch.planned.value = 1;
    expect(batch.canSubmit.value).toBe(false);
    batch.planned.value = 2;
    expect(batch.canSubmit.value).toBe(true);
  });

  it("resets deployment state when closed", () => {
    const deploy = useCloudNodeDeploy();
    deploy.open("pkg-1");
    expect(deploy.visible.value).toBe(true);
    deploy.close();
    expect(deploy.visible.value).toBe(false);
    expect(deploy.selectedPackageId.value).toBe("pkg-1");
  });
});
