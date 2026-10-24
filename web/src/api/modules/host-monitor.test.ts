import { describe, expect, it } from "vitest";
import { formatBytes, toHostMetrics } from "./host-monitor";

describe("host monitor wire conversion", () => {
  it("converts protobuf JSON uint64 strings into numeric metrics", () => {
    const metrics = toHostMetrics({
      agent_id: "agent-1",
      last_seen_at: new Date().toISOString(),
      snapshot: {
        memory: {
          total_bytes: "8589934592",
          used_bytes: "4294967296",
          available_bytes: "4294967296",
          usage_percent: 50
        },
        filesystems: [
          {
            device: "/dev/vda2",
            mountpoint: "/",
            fs_type: "ext4",
            total_bytes: "107374182400",
            used_bytes: "53687091200",
            available_bytes: "53687091200",
            usage_percent: 50
          }
        ],
        networks: [
          {
            device: "eth0",
            receive_errors_total: "2",
            transmit_errors_total: "3"
          }
        ]
      }
    } as any);

    expect(metrics.memory.total).toBe(8589934592);
    expect(metrics.memory.percent_available).toBe(true);
    expect(metrics.filesystems[0].total).toBe(107374182400);
    expect(metrics.filesystems[0].percent_available).toBe(true);
    expect(metrics.networks[0].receive_errors_total + metrics.networks[0].transmit_errors_total).toBe(5);
    expect(formatBytes(metrics.memory.total)).toBe("8 GB");
  });
});

describe("host monitor identity mapping", () => {
  it("keeps the agent id and the registered host id apart and never fakes an address", () => {
    const metrics = toHostMetrics({ agent_id: "pwU2", host_id: "control", hostname: "VM-0-1" } as any);
    expect(metrics).toMatchObject({ agent_id: "pwU2", host_id: "control", hostname: "VM-0-1" });
    expect(metrics).not.toHaveProperty("address");
  });

  it("leaves host_id empty for an agent that has no registered host", () => {
    expect(toHostMetrics({ agent_id: "stray" } as any)).toMatchObject({ agent_id: "stray", host_id: "", hostname: "" });
  });
});
