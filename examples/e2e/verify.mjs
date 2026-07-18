#!/usr/bin/env node
import { createCipheriv, createHash, randomBytes } from "node:crypto";
import { setTimeout as sleep } from "node:timers/promises";

const APP_INFO = {
  app_id: "moox_frontend",
  app_key: "2521e0d21b6be0347b72bca93904a0dd",
};

const PUBLIC_DEPLOYMENTS = new Set([
  "admin_gateway",
  "service_gateway",
  "web_host",
  "storage-primary",
  "storage-view",
]);

const JOB_STATUS = {
  1: "JOB_ITEM_STATUS_PENDING",
  2: "JOB_ITEM_STATUS_RUNNING",
  3: "JOB_ITEM_STATUS_SUCCESS",
  4: "JOB_ITEM_STATUS_FAILED",
  5: "JOB_ITEM_STATUS_CANCELED",
};

const TASK_STATUS = {
  1: "TASK_INSTANCE_STATUS_PENDING",
  2: "TASK_INSTANCE_STATUS_RUNNING",
  3: "TASK_INSTANCE_STATUS_SUCCESS",
  4: "TASK_INSTANCE_STATUS_PART_FAILED",
  5: "TASK_INSTANCE_STATUS_FAILED",
};

class FatalAssertionError extends Error {}

function parseArgs(argv) {
  const args = {
    phase: "prepare",
    gateway: "http://127.0.0.1:11000",
    health: "http://127.0.0.1:11010/healthz",
    web: "http://127.0.0.1:9527",
    host: "127.0.0.1",
    space: "crypto",
    rule: "binance_spot_kline_1h",
    node: "e2e-scf-node",
    package: "moox-collector_dev",
    dataset: "binance_spot_kline_1h",
    username: "mooxe2eadmin",
    password: "MooxE2E#20260704!",
    timeoutSeconds: 180,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i];
    const value = argv[i + 1];
    switch (key) {
      case "--phase":
      case "--gateway":
      case "--health":
      case "--web":
      case "--host":
      case "--space":
      case "--rule":
      case "--node":
      case "--package":
      case "--dataset":
      case "--username":
      case "--password":
        if (!value) throw new Error(`${key} requires a value`);
        args[key.slice(2)] = value;
        i += 1;
        break;
      case "--timeout-seconds":
        if (!value) throw new Error(`${key} requires a value`);
        args.timeoutSeconds = Number(value);
        i += 1;
        break;
      case "-h":
      case "--help":
        usage();
        process.exit(0);
        break;
      default:
        throw new Error(`unknown option: ${key}`);
    }
  }
  args.gateway = trimRight(args.gateway, "/");
  args.web = trimRight(args.web, "/");
  if (!Number.isFinite(args.timeoutSeconds) || args.timeoutSeconds <= 0) {
    throw new Error("--timeout-seconds must be positive");
  }
  return args;
}

function usage() {
  console.log(`Usage:
  node examples/e2e/verify.mjs --phase sysdeploy|prepare|assert [options]

Options:
  --gateway <url>          Admin gateway URL. Default: http://127.0.0.1:11000
  --web <url>              Web host URL. Default: http://127.0.0.1:9527
  --host <host>            Public host written to SysDeploy public endpoints.
  --space <space_id>       Space ID. Default: crypto
  --rule <rule_id>         Collector rule ID. Default: binance_spot_kline_1h
  --node <node_id>         Cloud runtime node ID. Default: e2e-scf-node
  --package <package_id>   Code package ID. Default: moox-collector_dev
  --dataset <dataset_id>   Dataset ID. Default: binance_spot_kline_1h
  --timeout-seconds <n>    Poll timeout for assert phase. Default: 180`);
}

function trimRight(value, suffix) {
  let out = String(value || "");
  while (out.endsWith(suffix)) out = out.slice(0, -suffix.length);
  return out;
}

function log(message) {
  console.log(`[e2e] ${message}`);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.phase === "sysdeploy" || args.phase === "prepare") {
    await prepare(args);
    return;
  }
  if (args.phase === "assert") {
    await assertAfterSCF(args);
    return;
  }
  throw new Error(`unsupported phase: ${args.phase}`);
}

async function prepare(args) {
  log("waiting for web host and admin gateway");
  await waitFor("web host", 60_000, async () => {
    await assertWebHost(args.web);
    return true;
  });
  const token = args.phase === "sysdeploy" ? "" : await login(args);
  await updatePublicDeployments(args, token);
  await assertSysDeploySingleInstanceContract(args, token);
  if (args.phase === "sysdeploy") {
    log("sysdeploy end-to-end test passed");
    return;
  }
  await ensureSpace(args, token);
  await ensureDatasetSubject(args, token);

  await assertManagementRequests(args, token);
  await ensureCloudNode(args, token);
  await ensureCollectorRule(args, token);
  await adminPost(args, token, "collectmgr", "RecalculateAllTaskInstances", { space_id: args.space });

  const instances = await waitFor("collector task instances", 60_000, async () => {
    const rows = await listTaskInstances(args, token);
    if (rows.length === 0) {
      throw new Error("collector has not generated task instances yet");
    }
    const withoutJob = rows.filter((item) => !item.cloud_job_item_id);
    if (withoutJob.length > 0) {
      throw new Error(`${withoutJob.length}/${rows.length} task instances do not have cloud_job_item_id yet`);
    }
    return rows;
  });

  const jobItems = await waitFor("cloudnode job items", 60_000, async () => {
    const rows = await listJobItems(args, token);
    if (rows.length === 0) throw new Error("cloudnode has no job items for collector rule");
    return rows;
  });

  log(`prepare ok: instances=${instances.length}, job_items=${jobItems.length}`);
}

async function assertAfterSCF(args) {
  const token = await login(args);
  await updatePublicDeployments(args, token);
  const timeoutMs = args.timeoutSeconds * 1000;

  const jobItems = await waitFor("cloudnode job items success", timeoutMs, async () => {
    const rows = await listJobItems(args, token);
    if (rows.length === 0) throw new Error("cloudnode job items are empty");
    const failed = rows.filter((item) => isJobStatus(item.status, "JOB_ITEM_STATUS_FAILED") || isJobStatus(item.status, "JOB_ITEM_STATUS_CANCELED"));
    if (failed.length > 0) {
      throw new FatalAssertionError(`job item failed: ${failed.map((item) => `${item.job_item_id}:${statusName(item.status, JOB_STATUS)}:${item.last_error_message || ""}`).join(", ")}`);
    }
    const pending = rows.filter((item) => !isJobStatus(item.status, "JOB_ITEM_STATUS_SUCCESS"));
    if (pending.length > 0) {
      throw new Error(`${pending.length}/${rows.length} job items are not success yet`);
    }
    return rows;
  });

  const instances = await waitFor("collector task instances success", timeoutMs, async () => {
    const rows = await listTaskInstances(args, token);
    if (rows.length === 0) throw new Error("collector task instances are empty");
    const failed = rows.filter((item) =>
      isTaskStatus(item.last_exec_status, "TASK_INSTANCE_STATUS_FAILED") ||
      isTaskStatus(item.last_exec_status, "TASK_INSTANCE_STATUS_PART_FAILED"),
    );
    if (failed.length > 0) {
      throw new FatalAssertionError(`task instance failed: ${failed.map((item) => `${item.task_id}:${statusName(item.last_exec_status, TASK_STATUS)}`).join(", ")}`);
    }
    const pending = rows.filter((item) => !isTaskStatus(item.last_exec_status, "TASK_INSTANCE_STATUS_SUCCESS"));
    if (pending.length > 0) {
      throw new Error(`${pending.length}/${rows.length} task instances are not success yet`);
    }
    return rows;
  });

  const rows = await waitFor("storage kline rows", timeoutMs, async () => {
    const rsp = await storagePost(args, token, "access", "ReadTimeSeriesRows", {
      keys: [{ space_id: args.space, dataset_id: args.dataset }],
      page: { page: 1, size: 20 },
    });
    const dataRows = rsp.rows || [];
    if (dataRows.length === 0) {
      throw new Error("storage has no time-series rows for binance spot kline dataset yet");
    }
    return dataRows;
  });

  const sample = rows[0]?.key || {};
  log(`assert ok: job_items=${jobItems.length}, instances=${instances.length}, storage_rows=${rows.length}, sample=${sample.subject_id || "-"}:${sample.freq || "-"}:${sample.data_time || "-"}`);
}

async function assertWebHost(webURL) {
  const rsp = await fetchText(`${webURL}/`, { method: "GET" }, "web host");
  if (!rsp.text.includes("<html") && !rsp.text.includes("id=\"app\"")) {
    throw new Error("web host did not return frontend html");
  }
}

async function assertGatewayHealth(healthURL) {
  const rsp = await fetchJSON(healthURL, { method: "GET" }, "admin health");
  if (rsp.status !== "ok" && rsp.data?.status !== "ok") {
    throw new Error(`admin health failed: ${JSON.stringify(rsp.data)}`);
  }
}

async function ensureUser(args) {
  const rsp = await adminPost(args, "", "auth", "Register", {
    app_info: APP_INFO,
    username: args.username,
    password: args.password,
    nickname: "MooX E2E",
    email: "moox-e2e@example.local",
  }, { raw: true });
  if (retOK(rsp.ret_info)) {
    log(`registered admin user: ${args.username}`);
    return;
  }
  const msg = retMsg(rsp.ret_info);
  if (msg.includes("已存在") || msg.toLowerCase().includes("exists")) {
    log(`admin user exists: ${args.username}`);
    return;
  }
  throw new Error(`register user failed: ${msg}`);
}

async function login(args) {
  const saltRsp = await adminPost(args, "", "auth", "GetLoginSalt", {
    app_info: APP_INFO,
    username: args.username,
  });
  const encrypted = encryptPassword(args.password, saltRsp.salt, saltRsp.timestamp);
  const loginRsp = await adminPost(args, "", "auth", "Login", {
    app_info: APP_INFO,
    username: args.username,
    password_hash: encrypted,
    salt: saltRsp.salt,
    timestamp: saltRsp.timestamp,
    device_id: "moox-e2e-node",
    user_agent: "moox-e2e",
    client_ip: "127.0.0.1",
  });
  if (!loginRsp.access_token) {
    throw new Error("login response missing access_token");
  }
  return loginRsp.access_token;
}

function encryptPassword(password, salt, timestamp) {
  const key = createHash("sha256").update(`${salt}${timestamp}`).digest();
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const encrypted = Buffer.concat([cipher.update(password, "utf8"), cipher.final()]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([iv, encrypted, tag]).toString("base64");
}

async function updatePublicDeployments(args, token) {
  const rsp = await adminPost(args, token, "sysdeploy", "ListServiceDeployments", {
    page: { page: 1, size: 200 },
  });
  const deployments = rsp.deployments || [];
  const updates = deployments.filter((item) => PUBLIC_DEPLOYMENTS.has(item.service_name));
  for (const item of updates) {
    const next = deploymentInput({ ...item, host: args.host, status: "active" });
    await adminPost(args, token, "sysdeploy", "UpdateServiceDeployment", {
      service_name: item.service_name,
      deployment: next,
    });
  }
  log(`public service deployments host=${args.host}, updated=${updates.length}`);
}

function deploymentInput(item) {
  return {
    service_name: item.service_name,
    service_kind: item.service_kind,
    protocol: item.protocol,
    host: item.host,
    port: item.port,
    gateway_path: item.gateway_path || "",
    scope: item.scope,
    status: item.status,
    description: item.description || "",
    extra_config: item.extra_config || "{}",
  };
}

async function assertSysDeploySingleInstanceContract(args, token) {
  const serviceName = `e2e_sysdeploy_${randomBytes(8).toString("hex")}`;
  const first = {
    service_name: serviceName,
    service_kind: "e2e",
    protocol: "http",
    host: "127.0.0.1",
    port: 19090,
    gateway_path: "trpc.moox.e2e.SysDeploy",
    scope: "internal",
    status: "active",
    description: "temporary SysDeploy E2E row",
    extra_config: "{}",
  };
  try {
    await adminPost(args, token, "sysdeploy", "CreateServiceDeployment", { deployment: first });
    const update = {
      ...first,
      host: "127.0.0.2",
      port: 19091,
      base_url: "http://127.0.0.1:19090",
      rpc_address: "127.0.0.1:19090",
    };
    await adminPost(args, token, "sysdeploy", "UpdateServiceDeployment", {
      service_name: serviceName,
      deployment: update,
    });
    const got = await adminPost(args, token, "sysdeploy", "GetServiceDeployment", { service_name: serviceName });
    if (got.deployment?.base_url !== "http://127.0.0.2:19091" || got.deployment?.rpc_address !== "127.0.0.2:19091") {
      throw new Error(`sysdeploy returned stale derived address: ${JSON.stringify(got.deployment)}`);
    }

    await adminPost(args, token, "sysdeploy", "DeleteServiceDeployment", { service_name: serviceName });
    await adminPost(args, token, "sysdeploy", "CreateServiceDeployment", { deployment: first });
    await adminPost(args, token, "sysdeploy", "DeleteServiceDeployment", { service_name: serviceName });

    const active = await adminPost(args, token, "sysdeploy", "ListActiveServiceDeployments", {});
    if (active.deployment_map?.[serviceName]) {
      throw new Error("deleted SysDeploy E2E row remains active");
    }
    log("sysdeploy single-instance contract: ok");
  } catch (err) {
    try {
      await deleteDeploymentIfPresent(args, token, serviceName);
    } catch (cleanupErr) {
      log(`sysdeploy cleanup failed: ${cleanupErr?.message || cleanupErr}`);
    }
    throw err;
  }
}

async function deleteDeploymentIfPresent(args, token, serviceName) {
  const listed = await adminPost(args, token, "sysdeploy", "ListServiceDeployments", {
    service_name: serviceName,
    page: { page: 1, size: 10 },
  });
  if ((listed.deployments || []).some((item) => item.service_name === serviceName)) {
    await adminPost(args, token, "sysdeploy", "DeleteServiceDeployment", { service_name: serviceName });
  }
}

async function ensureSpace(args, token) {
  const rsp = await adminPost(args, token, "space", "ListSpaces", {
    page: { page: 1, size: 200 },
  });
  const exists = (rsp.spaces || []).some((item) => item.space_id === args.space);
  if (exists) {
    log(`space exists: ${args.space}`);
    return;
  }
  await adminPost(args, token, "space", "CreateSpace", {
    space: {
      space_id: args.space,
      name: "Crypto",
      description: "MooX E2E crypto space",
      owner: args.username,
      market: "crypto",
      timezone: "Asia/Shanghai",
      status: "active",
      attributes: "{}",
    },
  });
  log(`created space: ${args.space}`);
}

async function ensureDatasetSubject(args, token) {
  await storagePost(args, token, "metadata", "RegisterDataSubject", {
    space_id: args.space,
    data_source_id: "binance",
    external_symbol: "BTCUSDT",
    subject: {
      subject_id: "BTC-USDT",
      subject_type: "crypto_pair",
      name: "Bitcoin / Tether",
      market: "crypto",
      currency: "USDT",
      timezone: "UTC",
      status: "active",
    },
    dataset_bindings: [{
      dataset_id: args.dataset,
      subject_role: "normal",
      status: "active",
    }],
  });
  log(`dataset subject ready: ${args.dataset}/BTC-USDT`);
}

async function assertManagementRequests(args, token) {
  const spaces = await adminPost(args, token, "space", "ListSpaces", { page: { page: 1, size: 50 } });
  if (!(spaces.spaces || []).some((item) => item.space_id === args.space)) {
    throw new Error(`space ${args.space} not visible through admin gateway`);
  }

  const active = await adminPost(args, token, "sysdeploy", "ListActiveServiceDeployments", {});
  if (!active.deployment_map?.["storage-primary"] || !active.deployment_map?.["storage-view"]) {
    throw new Error("sysdeploy active deployment map missing storage endpoints");
  }

  const dataTypes = await adminPost(args, token, "collectmgr", "GetDataTypeConfigs", {});
  if ((dataTypes.configs || []).length === 0) {
    throw new Error("collector data type configs are empty");
  }

  const nodes = await adminPost(args, token, "cloudnode", "GetNodeList", { page: { page: 1, size: 20 } }, { spaceId: args.space });
  if (!Array.isArray(nodes.items)) {
    throw new Error("cloudnode GetNodeList response missing items");
  }

  const datasets = await storagePost(args, token, "metadata", "ListDatasets", {
    space_id: args.space,
    page: { page: 1, size: 50 },
  });
  if (!(datasets.datasets || []).some((item) => item.dataset_id === args.dataset)) {
    throw new Error(`dataset ${args.dataset} missing; metadata seed was not imported`);
  }

  const subjects = await storagePost(args, token, "metadata", "ListDatasetSubjects", {
    space_id: args.space,
    dataset_id: args.dataset,
    page: { page: 1, size: 100 },
  });
  if ((subjects.dataset_subjects || []).length === 0) {
    throw new Error(`dataset ${args.dataset} has no subjects; metadata seed was not imported`);
  }
  log(`management requests ok: subjects=${subjects.dataset_subjects.length}`);
}

async function ensureCloudNode(args, token) {
  const rsp = await adminPost(args, token, "cloudnode", "BatchCreateNodes", {
    nodes: [{
      cloud_account_id: "e2e-local",
      node_type: "scf-event",
      runtime: "go1",
      handler: "main",
      region: "local",
      namespace: "e2e",
      package_id: args.package,
      deployment_id: args.package,
      metadata: {
        node_id: args.node,
        function_name: args.node,
        function_name_prefix: "e2e-scf",
        supported_workloads: ["collect.kline", "collect.symbol"],
      },
    }],
  }, { spaceId: args.space });
  if ((rsp.processed_count || 0) < 1) {
    throw new Error("BatchCreateNodes did not process the e2e node");
  }
  log(`cloudnode runtime node ready: ${args.node}`);
}

async function ensureCollectorRule(args, token) {
  const rule = {
    space_id: args.space,
    rule_id: args.rule,
    data_type: "kline",
    exchange: "binance",
    collect_params: {
      source: { kind: "dataset_subjects", dataset_id: args.dataset },
      collector: { exchange: "binance", market: "spot", data_type: "kline", intervals: ["1h"] },
      target: { dataset_id: args.dataset, job_type: "collect.kline", code_package_id: args.package },
      schedule: { interval: "1h", timezone: "Asia/Shanghai" },
    },
    assignment_type: "auto",
    assigned_nodes: [],
    node_pattern: "",
    node_tags: [],
    enabled: true,
    creator: "moox-e2e",
  };

  const detail = await adminPost(args, token, "collectmgr", "GetTaskRuleDetail", {
    space_id: args.space,
    rule_id: args.rule,
  }, { raw: true });
  if (retOK(detail.ret_info)) {
    await adminPost(args, token, "collectmgr", "UpdateTaskRule", {
      space_id: args.space,
      rule_id: args.rule,
      rule,
    });
    log(`updated collector rule: ${args.rule}`);
    return;
  }
  await adminPost(args, token, "collectmgr", "CreateTaskRule", { rule });
  log(`created collector rule: ${args.rule}`);
}

async function listTaskInstances(args, token) {
  const rsp = await adminPost(args, token, "collectmgr", "GetTaskInstanceList", {
    filter: {
      space_id: args.space,
      rule_id: args.rule,
      page: { page: 1, size: 200 },
    },
  });
  return rsp.instances || [];
}

async function listJobItems(args, token) {
  const rsp = await adminPost(args, token, "cloudnode", "ListJobItems", {
    space_id: args.space,
    job_id: args.rule,
    page: { page: 1, size: 200 },
  }, { spaceId: args.space });
  return rsp.items || [];
}

async function storagePost(args, token, group, method, body) {
  const service = {
    metadata: "storage-primary",
    access: "storage-primary",
    view: "storage-view",
  }[group];
  if (!service) throw new Error(`unknown storage group: ${group}`);
  return adminPost(args, token, "storage", method, {
    auth_info: {
      ...APP_INFO,
      operator: "moox_e2e",
      request_id: `e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    },
    ...body,
  });
}

async function adminPost(args, token, service, method, body, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    "User-Agent": "moox-e2e",
    "X-Trace-Id": `e2e-${service}-${method}-${Date.now()}`,
  };
  if (token) {
    headers.Authorization = token;
    headers["X-Access-Token"] = token;
  }
  if (options.spaceId) {
    headers["X-Space-Id"] = options.spaceId;
  }
  const url = args.phase === "sysdeploy" && service === "sysdeploy"
    ? `http://127.0.0.1:11109/trpc.moox.ops.SysDeploy/${method}`
    : `${args.gateway}/api/admin/${service === "storage" ? "storage" : service}/${method}`;
  const rsp = await fetchJSON(url, {
    method: "POST",
    headers,
    body: JSON.stringify(body || {}),
  }, `${service}/${method}`);
  if (options.raw) return rsp.data;
  assertRet(rsp.data, `${service}/${method}`);
  return rsp.data;
}

async function fetchJSON(url, options, op) {
  const rsp = await fetch(url, options);
  const text = await rsp.text();
  let data = null;
  if (text.trim()) {
    try {
      data = JSON.parse(text);
    } catch (err) {
      throw new Error(`${op} returned non-json response: ${text.slice(0, 300)}`);
    }
  }
  if (!rsp.ok) {
    throw new Error(`${op} failed status=${rsp.status} body=${text.slice(0, 300)}`);
  }
  return { data, text, headers: rsp.headers };
}

async function fetchText(url, options, op) {
  const rsp = await fetch(url, options);
  const text = await rsp.text();
  if (!rsp.ok) {
    throw new Error(`${op} failed status=${rsp.status} body=${text.slice(0, 300)}`);
  }
  return { text, headers: rsp.headers };
}

async function waitFor(label, timeoutMs, fn) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const value = await fn();
      log(`${label}: ok`);
      return value;
    } catch (err) {
      if (err instanceof FatalAssertionError) throw err;
      lastError = err;
      await sleep(2000);
    }
  }
  throw new Error(`${label}: timed out after ${Math.round(timeoutMs / 1000)}s; last error: ${lastError?.message || "none"}`);
}

function assertRet(data, op) {
  if (!data || !data.ret_info) {
    throw new Error(`${op} response missing ret_info: ${JSON.stringify(data)}`);
  }
  if (!retOK(data.ret_info)) {
    throw new Error(`${op} failed: ${retMsg(data.ret_info)}`);
  }
}

function retOK(retInfo) {
  if (!retInfo) return false;
  const code = retInfo.code;
  return code === 0 || code === "0" || code === "SUCCESS" || code === "ErrorCode_SUCCESS";
}

function retMsg(retInfo) {
  if (!retInfo) return "missing ret_info";
  return retInfo.msg || `code=${retInfo.code}`;
}

function statusName(value, table) {
  if (typeof value === "number") return table[value] || String(value);
  if (typeof value === "string" && /^\d+$/.test(value)) return table[Number(value)] || value;
  return String(value || "");
}

function isJobStatus(value, expected) {
  return statusName(value, JOB_STATUS) === expected;
}

function isTaskStatus(value, expected) {
  return statusName(value, TASK_STATUS) === expected;
}

main().catch((err) => {
  console.error(`[e2e] ERROR: ${err.message}`);
  process.exit(1);
});
