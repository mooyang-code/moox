import { describe, expect, it } from "vitest";
import type { HostMetrics } from "@/api/modules/host-monitor";
import type { SSHHost } from "@/api/modules/ssh";
import { buildHostMonitorRows, normalizeMonitorViewMode } from "./host-monitor-mapping";

function monitor(overrides: Partial<HostMetrics> = {}): HostMetrics {
  return {
    host_id: "agent-1",
    host_name: "prod-a",
    address: "agent-1",
    status: "online",
    timestamp: "2026-07-15T02:00:00Z",
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
    storage_available: true,
    ...overrides
  };
}

function ssh(overrides: Partial<SSHHost> = {}): SSHHost {
  return {
    id: 1,
    name: "prod-a",
    address: "10.0.0.1",
    port: 22,
    user: "ubuntu",
    auth_type: "pwd",
    net_type: "tcp4",
    font_size: 14,
    background: "#000000",
    foreground: "#ffffff",
    cursor_color: "#ffffff",
    font_family: "monospace",
    cursor_style: "block",
    shell: "/bin/bash",
    pty_type: "xterm-256color",
    ...overrides
  };
}

describe("buildHostMonitorRows", () => {
  it("matches a monitor agent to an SSH host by exact unique name", () => {
    const rows = buildHostMonitorRows(
      [monitor({ host_name: "腾讯云-122" })],
      [ssh({ name: "腾讯云-122", address: "106.53.107.122" })]
    );

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      match: "unique_name",
      displayName: "腾讯云-122",
      displayAddress: "106.53.107.122",
      state: "online"
    });
  });

  it("prefers exact address matching before a different display name", () => {
    const rows = buildHostMonitorRows(
      [monitor({ address: "43.132.204.177", host_name: "VM-32-13-ubuntu" })],
      [ssh({ name: "腾讯云-香港", address: "43.132.204.177" })]
    );

    expect(rows[0].match).toBe("address");
    expect(rows[0].displayName).toBe("腾讯云-香港");
  });

  it("does not match duplicate names and preserves all inventory rows", () => {
    const rows = buildHostMonitorRows(
      [monitor({ host_id: "agent-a", host_name: "duplicate" })],
      [ssh({ id: 1, name: "duplicate" }), ssh({ id: 2, name: "duplicate", address: "10.0.0.2" })]
    );

    expect(rows).toHaveLength(3);
    expect(rows[0].match).toBe("unmatched");
    expect(rows.filter(row => row.state === "unmonitored")).toHaveLength(2);
  });

  it("keeps SSH-only and agent-only rows with explicit states", () => {
    const rows = buildHostMonitorRows(
      [monitor({ host_id: "agent-only", host_name: "agent-only", address: "agent-only" })],
      [ssh({ id: 9, name: "ssh-only", address: "10.0.0.9" })]
    );

    expect(rows.map(row => row.state)).toEqual(["online", "unmonitored"]);
    expect(rows[0].displayAddress).toBe("agent-only");
    expect(rows[1].displayAddress).toBe("10.0.0.9");
  });

  it("derives summaries, attention, and aggregate network rates", () => {
    const rows = buildHostMonitorRows(
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
      []
    );

    expect(rows[0]).toMatchObject({
      cpuUsage: 81,
      memoryUsage: 38,
      filesystemUsage: 78,
      networkRate: { rx: 120, tx: 50 },
      attention: true
    });
  });

  it("does not render offline or unavailable values as zero", () => {
    const rows = buildHostMonitorRows(
      [
        monitor({
          status: "offline",
          cpu: { usage: 0, usage_available: true, cores: 4 },
          memory: { total: 100, used: 0, available: 100, percent: 0, percent_available: true }
        })
      ],
      []
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
