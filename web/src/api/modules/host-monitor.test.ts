import { describe, expect, it } from "vitest";
import { formatBytes, toHostMetrics } from "./host-monitor";

describe("host monitor wire conversion", () => {
  it("converts protobuf JSON uint64 strings into numeric metrics", () => {
    const metrics = toHostMetrics({
      host_id: "control",
      last_seen_at: String(Date.now()),
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
        ],
        top_processes: [
          { pid: 11, name: "moox-storage-primary", cpu_percent: 340.4, cpu_available: true, rss_bytes: "5368709120" },
          { pid: 12, name: "fresh", rss_bytes: 1048576 }
        ]
      }
    } as any);

    expect(metrics.processes).toEqual([
      { pid: 11, name: "moox-storage-primary", cpu_percent: 340.4, cpu_available: true, rss: 5368709120 },
      { pid: 12, name: "fresh", cpu_percent: 0, cpu_available: false, rss: 1048576 }
    ]);

    expect(metrics.memory.total).toBe(8589934592);
    expect(metrics.memory.percent_available).toBe(true);
    expect(metrics.filesystems[0].total).toBe(107374182400);
    expect(metrics.filesystems[0].percent_available).toBe(true);
    expect(metrics.networks[0].receive_errors_total + metrics.networks[0].transmit_errors_total).toBe(5);
    expect(formatBytes(metrics.memory.total)).toBe("8 GB");
  });
});

describe("host monitor identity mapping", () => {
  it("keeps the registered host id and never fakes an address", () => {
    const metrics = toHostMetrics({ host_id: "control", hostname: "VM-0-1" });
    expect(metrics).toMatchObject({ host_id: "control", hostname: "VM-0-1" });
    expect(metrics).not.toHaveProperty("address");
  });
});
