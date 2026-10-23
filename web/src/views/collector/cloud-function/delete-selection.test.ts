import { describe, expect, it } from "vitest";
import { captureDeleteSelection, updateDeleteSelection } from "./delete-selection";
import type { FunctionInstance } from "./function-instance-model";

function instance(instance_id: string, version: string): FunctionInstance {
  return {
    instance_id,
    cloud_account_id: "account-1",
    namespace: "collector",
    function_type: "scf-event",
    trigger_type: "invoke",
    region: "ap-singapore",
    package_id: `pkg-${version}`,
    deployment_id: `deploy-${version}`,
    metadata: {},
    modify_time: `2026-10-03T10:00:${version === "old" ? "00" : "01"}Z`
  };
}

describe("cloud instance delete selection snapshots", () => {
  it("keeps immutable row identities across pages and captures the confirmed targets", () => {
    let snapshots = updateDeleteSelection({}, [instance("instance-a", "old")], true);
    snapshots = updateDeleteSelection(snapshots, [instance("instance-b", "old")], true);

    // Refreshing a selected row on another page must not retarget the pending selection.
    snapshots = updateDeleteSelection(snapshots, [instance("instance-a", "new")], true);
    expect(captureDeleteSelection(["instance-a", "instance-b"], snapshots)).toEqual([
      {
        instance_id: "instance-a",
        package_id: "pkg-old",
        deployment_id: "deploy-old",
        modify_time: "2026-10-03T10:00:00Z"
      },
      {
        instance_id: "instance-b",
        package_id: "pkg-old",
        deployment_id: "deploy-old",
        modify_time: "2026-10-03T10:00:00Z"
      }
    ]);
  });

  it("removes only the deselected page rows", () => {
    let snapshots = updateDeleteSelection({}, [instance("instance-a", "old"), instance("instance-b", "old")], true);
    snapshots = updateDeleteSelection(snapshots, [instance("instance-a", "old")], false);
    expect(captureDeleteSelection(["instance-b"], snapshots)).toHaveLength(1);
    expect(() => captureDeleteSelection(["instance-a"], snapshots)).toThrow("缺少选择时的身份快照");
  });
});
