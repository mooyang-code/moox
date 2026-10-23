import { describe, expect, it } from "vitest";
import { captureDeleteSelection, updateDeleteSelection } from "./delete-selection";
import type { CloudNode } from "./cloud-node-model";

function node(node_id: string, version: string): CloudNode {
  return {
    node_id,
    cloud_account_id: "account-1",
    namespace: "collector",
    node_type: "scf-event",
    trigger_type: "invoke",
    region: "ap-singapore",
    tag: "",
    ip_address: "",
    package_id: `pkg-${version}`,
    deployment_id: `deploy-${version}`,
    metadata: {},
    modify_time: `2026-10-03T10:00:${version === "old" ? "00" : "01"}Z`
  };
}

describe("cloud node delete selection snapshots", () => {
  it("keeps immutable row identities across pages and captures the confirmed targets", () => {
    let snapshots = updateDeleteSelection({}, [node("node-a", "old")], true);
    snapshots = updateDeleteSelection(snapshots, [node("node-b", "old")], true);

    // Refreshing a selected row on another page must not retarget the pending selection.
    snapshots = updateDeleteSelection(snapshots, [node("node-a", "new")], true);
    expect(captureDeleteSelection(["node-a", "node-b"], snapshots)).toEqual([
      {
        node_id: "node-a",
        package_id: "pkg-old",
        deployment_id: "deploy-old",
        modify_time: "2026-10-03T10:00:00Z"
      },
      {
        node_id: "node-b",
        package_id: "pkg-old",
        deployment_id: "deploy-old",
        modify_time: "2026-10-03T10:00:00Z"
      }
    ]);
  });

  it("removes only the deselected page rows", () => {
    let snapshots = updateDeleteSelection({}, [node("node-a", "old"), node("node-b", "old")], true);
    snapshots = updateDeleteSelection(snapshots, [node("node-a", "old")], false);
    expect(captureDeleteSelection(["node-b"], snapshots)).toHaveLength(1);
    expect(() => captureDeleteSelection(["node-a"], snapshots)).toThrow("缺少选择时的身份快照");
  });
});
