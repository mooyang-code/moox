import { describe, expect, it } from "vitest";
import type { HostAlertThresholds, HostMetrics } from "@/api/modules/host-monitor";
import type { DeploymentHost } from "@/api/admin/types";
import { buildHostMonitorRows, normalizeMonitorViewMode } from "./host-monitor-mapping";

const defaultThresholds: HostAlertThresholds = { cpu: 80, memory: 90, disk: 85 };

function monitor(overrides: Partial<HostMetrics> = {}): HostMetrics {
  return {
    host_id: "control",
    hostname: "VM-0-1-ubuntu",
    status: "online",
    timestamp: "1784080800000",
    cpu: { usage: 24, usage_available: true, cores: 4 },
    memory: { total: 100, used: 38, available: 62, percent: 38, percent_available: true },
    filesystems: [
      {
        device: "/dev/vda1",
        mountpoint: "/",
        fs_type: "ext4",
        total: 100,
        used: 67,
        available: 33,
        percent: 67,
        percent_available: true,
        read_only: false
      }
    ],
    disks: [],
    networks: [
      {
        device: "eth0",
        operstate: "up",
        rx_speed: 100,
        tx_speed: 40,
        receive_errors_total: 0,
        transmit_errors_total: 0,
        rate_available: true
      }
    ],
    processes: [],
    ...overrides
  };
}

function host(overrides: Partial<DeploymentHost> = {}): DeploymentHost {
  return { host_id: "control", label: "控制机", address: "203.0.113.10", status: "enabled", ssh_port: 22, ...overrides };
}

describe("buildHostMonitorRows", () => {
  it("lists registered hosts with the Chinese label, host id and address", () => {
    const rows = buildHostMonitorRows([host()], [monitor()]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      key: "control",
      hostId: "control",
      displayName: "控制机",
      displayAddress: "203.0.113.10",
      state: "online"
    });
    expect(rows[0].monitor?.host_id).toBe("control");
  });

  it("falls back to the host id when no label is registered and shows unreported hosts", () => {
    const rows = buildHostMonitorRows([host({ host_id: "storage", label: "" })], []);
    expect(rows[0]).toMatchObject({ displayName: "storage", state: "unmonitored", monitor: undefined });
  });

  it("gives every registered host exactly one row and ignores reports from unregistered hosts", () => {
    const hosts = [host(), host({ host_id: "storage", label: "存储机" })];
    const rows = buildHostMonitorRows(hosts, [monitor({ host_id: "retired-host" }), monitor({ host_id: "storage" })]);
    expect(rows.map(row => row.key)).toEqual(["control", "storage"]);
    expect(rows.map(row => row.state)).toEqual(["unmonitored", "online"]);
  });

  it("derives summaries, attention, and aggregate network rates", () => {
    const rows = buildHostMonitorRows(
      [host()],
      [
        monitor({
          cpu: { usage: 81.2, usage_available: true, cores: 4 },
          filesystems: [
            {
              device: "/dev/a",
              mountpoint: "/",
              fs_type: "ext4",
              total: 100,
              used: 30,
              available: 70,
              percent: 30,
              percent_available: true,
              read_only: false
            },
            {
              device: "/dev/b",
              mountpoint: "/data",
              fs_type: "ext4",
              total: 100,
              used: 78,
              available: 22,
              percent: 78,
              percent_available: true,
              read_only: false
            }
          ],
          networks: [
            {
              device: "eth0",
              operstate: "up",
              rx_speed: 100,
              tx_speed: 40,
              receive_errors_total: 0,
              transmit_errors_total: 0,
              rate_available: true
            },
            {
              device: "eth1",
              operstate: "up",
              rx_speed: 20,
              tx_speed: 10,
              receive_errors_total: 0,
              transmit_errors_total: 0,
              rate_available: true
            }
          ]
        })
      ],
      defaultThresholds
    );

    expect(rows[0]).toMatchObject({
      cpuUsage: 81,
      memoryUsage: 38,
      filesystemUsage: 78,
      cpuExceeded: true,
      memoryExceeded: false,
      filesystemExceeded: false,
      networkRate: { rx: 120, tx: 50 },
      attention: true
    });
  });

  it("evaluates attention against backend thresholds per resource", () => {
    // 内存 85%、磁盘 84% 均低于各自告警阈值（90%、85%），不应标记为需关注。
    const normal = buildHostMonitorRows(
      [host()],
      [
        monitor({
          cpu: { usage: 79.6, usage_available: true, cores: 4 },
          memory: { total: 100, used: 85, available: 15, percent: 85, percent_available: true },
          filesystems: [
            {
              device: "/dev/vda1",
              mountpoint: "/",
              fs_type: "ext4",
              total: 100,
              used: 84,
              available: 16,
              percent: 84,
              percent_available: true,
              read_only: false
            }
          ]
        })
      ],
      defaultThresholds
    );
    expect(normal[0]).toMatchObject({
      cpuExceeded: false,
      memoryExceeded: false,
      filesystemExceeded: false,
      attention: false
    });

    // 内存达到 90% 阈值时需关注。
    const memHigh = buildHostMonitorRows(
      [host()],
      [monitor({ memory: { total: 100, used: 90, available: 10, percent: 90, percent_available: true } })],
      defaultThresholds
    );
    expect(memHigh[0]).toMatchObject({
      cpuExceeded: false,
      memoryExceeded: true,
      filesystemExceeded: false,
      attention: true
    });

    // 磁盘达到 85% 阈值时需关注。
    const diskHigh = buildHostMonitorRows(
      [host()],
      [
        monitor({
          filesystems: [
            {
              device: "/dev/vda1",
              mountpoint: "/",
              fs_type: "ext4",
              total: 100,
              used: 85,
              available: 15,
              percent: 85,
              percent_available: true,
              read_only: false
            }
          ]
        })
      ],
      defaultThresholds
    );
    expect(diskHigh[0]).toMatchObject({
      cpuExceeded: false,
      memoryExceeded: false,
      filesystemExceeded: true,
      attention: true
    });

    // 后端未下发阈值（0 或缺省）时不凭前端写死值误判需关注。
    const noThresholds = buildHostMonitorRows([host()], [monitor({ cpu: { usage: 95, usage_available: true, cores: 4 } })]);
    expect(noThresholds[0]).toMatchObject({
      cpuExceeded: false,
      memoryExceeded: false,
      filesystemExceeded: false,
      attention: false
    });
  });

  it("does not render offline or unavailable values as zero", () => {
    const rows = buildHostMonitorRows(
      [host()],
      [
        monitor({
          status: "offline",
          cpu: { usage: 0, usage_available: true, cores: 4 },
          memory: { total: 100, used: 0, available: 100, percent: 0, percent_available: true }
        })
      ]
    );

    expect(rows[0]).toMatchObject({
      state: "offline",
      cpuUsage: null,
      memoryUsage: null,
      filesystemUsage: null,
      networkRate: null,
      attention: false
    });
  });
});

describe("normalizeMonitorViewMode", () => {
  it("defaults to cards and accepts master", () => {
    expect(normalizeMonitorViewMode(null)).toBe("cards");
    expect(normalizeMonitorViewMode("unexpected")).toBe("cards");
    expect(normalizeMonitorViewMode("master")).toBe("master");
  });
});
