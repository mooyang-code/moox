import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));

import { importSCFFunctions, previewSCFFunctions, submitCreateNodes, submitDeployNodes } from "./cloud-node";

describe("cloud node batch API", () => {
  beforeEach(() => {
    callControl.mockReset();
  });

  it("submits create nodes and returns the backend job id", async () => {
    callControl.mockResolvedValue({
      job_id: "node-batch-create",
      operation: "NODE_BATCH_OPERATION_CREATE_NODES",
      total_count: 1
    });

    const result = await submitCreateNodes({
      nodes: [
        {
          cloud_account_id: "account-1",
          runtime: "Go1",
          region: "ap-guangzhou",
          package_id: "pkg-1",
          metadata: { function_name_prefix: "collector", index: 0 }
        }
      ],
      cloud_account_id: "account-1",
      region: "ap-guangzhou",
      namespace: "default",
      function_name_prefix: "collector",
      runtime: "Go1",
      count: 1
    });

    expect(result.job_id).toBe("node-batch-create");
    expect(callControl).toHaveBeenCalledWith("cloudnode", "SubmitCreateNodes", {
      nodes: expect.arrayContaining([expect.objectContaining({ package_id: "pkg-1" })])
    });
  });

  it("submits deployments and returns the backend job id", async () => {
    callControl.mockResolvedValue({
      job_id: "node-batch-deploy",
      operation: "NODE_BATCH_OPERATION_DEPLOY_NODES",
      total_count: 2
    });

    const result = await submitDeployNodes({ node_ids: ["node-1", "node-2"], package_id: "pkg-2" });

    expect(result.job_id).toBe("node-batch-deploy");
    expect(callControl).toHaveBeenCalledWith("cloudnode", "SubmitDeployNodes", {
      deployments: [
        { node_id: "node-1", package_id: "pkg-2" },
        { node_id: "node-2", package_id: "pkg-2" }
      ]
    });
  });

  it("previews and imports only selected SCF references", async () => {
    callControl.mockResolvedValueOnce({
      functions: [
        { function: { region: "ap-guangzhou", namespace: "default", function_name: "moox-fetcher" }, importable: true }
      ],
      region_errors: []
    });
    const preview = await previewSCFFunctions("account-1");
    expect(preview.functions).toHaveLength(1);
    expect(callControl).toHaveBeenNthCalledWith(
      1,
      "cloudnode",
      "PreviewSCFFunctions",
      { account_id: "account-1" },
      { timeout: 180000 }
    );
    callControl.mockResolvedValueOnce({ created: 1, restored: 0, unchanged: 0, failed: 0, results: [] });
    await importSCFFunctions("account-1", [preview.functions[0].function]);
    expect(callControl).toHaveBeenLastCalledWith(
      "cloudnode",
      "ImportSCFFunctions",
      {
        account_id: "account-1",
        functions: [{ region: "ap-guangzhou", namespace: "default", function_name: "moox-fetcher" }]
      },
      { timeout: 180000 }
    );
  });
});
