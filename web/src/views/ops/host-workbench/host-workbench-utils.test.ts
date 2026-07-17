import { describe, expect, it } from "vitest";
import { mergeHostWorkbenchRows } from "./host-workbench-utils";

describe("mergeHostWorkbenchRows", () => {
  it("matches monitor and ssh rows by exact address before unique name", () => {
    const rows = mergeHostWorkbenchRows(
      [
        {
          host_id: "agent-1",
          host_name: "prod-a",
          address: "10.0.0.1",
          status: "online",
          timestamp: "",
          cpu: {} as any,
          memory: {} as any,
          filesystems: [],
          disks: [],
          networks: [],
          storage_available: true
        }
      ],
      [{ id: 1, name: "prod-a", address: "10.0.0.1", port: 22, user: "root" } as any]
    );
    expect(rows[0].match).toBe("address");
    expect(rows[0].ssh?.id).toBe(1);
  });

  it("does not fuzzy match ambiguous names", () => {
    const rows = mergeHostWorkbenchRows(
      [
        {
          host_id: "agent-1",
          host_name: "prod",
          address: "agent-1",
          status: "offline",
          timestamp: "",
          cpu: {} as any,
          memory: {} as any,
          filesystems: [],
          disks: [],
          networks: [],
          storage_available: true
        }
      ],
      [
        { id: 1, name: "prod", address: "10.0.0.1" },
        { id: 2, name: "prod", address: "10.0.0.2" }
      ] as any
    );
    expect(rows[0].match).toBe("unmatched");
    expect(rows[0].ssh).toBeUndefined();
  });

  it("matches an agent display name to its SSH host", () => {
    const rows = mergeHostWorkbenchRows(
      [
        {
          host_id: "agent-1",
          host_name: "腾讯云-香港",
          address: "agent-1",
          status: "online",
          timestamp: "",
          cpu: {} as any,
          memory: {} as any,
          filesystems: [],
          disks: [],
          networks: [],
          storage_available: true
        }
      ],
      [{ id: 1, name: "腾讯云-香港", address: "43.132.204.177" }] as any
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].match).toBe("unique_name");
    expect(rows[0].ssh?.id).toBe(1);
  });
});
