#!/usr/bin/env node
import { createCipheriv, createHash, createHmac, randomBytes } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const APP_INFO = {
  app_id: "moox_frontend",
  app_key: "2521e0d21b6be0347b72bca93904a0dd",
};

export const PUBLIC_DEPLOYMENTS = new Set([
  "admin_gateway",
  "service_gateway",
  "web_host",
]);

export const JOB_STATUS = {
  1: "JOB_ITEM_STATUS_PENDING",
  3: "JOB_ITEM_STATUS_SUCCESS",
  4: "JOB_ITEM_STATUS_FAILED",
  6: "JOB_ITEM_STATUS_ENQUEUE_FAILED",
};

const NODE_STATUS = {
  1: "NODE_STATUS_OFFLINE",
  2: "NODE_STATUS_ONLINE",
  3: "NODE_STATUS_TIMEOUT",
  4: "NODE_STATUS_ABNORMAL",
};

const JOB_ERROR_KIND = {
  1: "JOB_ITEM_ERROR_KIND_RETRYABLE",
  2: "JOB_ITEM_ERROR_KIND_PERMANENT",
};

const TASK_STATUS = {
  1: "TASK_INSTANCE_STATUS_PENDING",
  2: "TASK_INSTANCE_STATUS_SUCCESS",
  3: "TASK_INSTANCE_STATUS_FAILED",
};

const BATCH_ACCEPTANCE_COUNT = 20;
const EXPECTED_BATCH_SIZE = 10;

class FatalAssertionError extends Error {}

export function parseArgs(argv) {
  const args = {
    phase: "setup",
    gateway: "http://127.0.0.1:11000",
    health: "http://127.0.0.1:11010/healthz",
    web: "http://127.0.0.1:9527",
    host: "127.0.0.1",
    space: "crypto",
    rule: "binance_spot_kline_1h",
    node: "e2e-scf-node",
    package: "moox-collector_dev",
    dataset: "binance_spot_kline_1h",
    e2eNodeId: "e2e-gateway",
    username: "mooxe2eadmin",
    password: "MooxE2E#20260704!",
    stateFile: "",
    timeoutSeconds: 120,
    skipCloudNodeSetup: false,
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
      case "--e2e-node":
        if (!value) throw new Error(`${key} requires a value`);
        args.e2eNodeId = value;
        i += 1;
        break;
      case "--state-file":
        if (!value) throw new Error(`${key} requires a value`);
        args.stateFile = value;
        i += 1;
        break;
      case "--timeout-seconds":
        if (!value) throw new Error(`${key} requires a value`);
        args.timeoutSeconds = Number(value);
        i += 1;
        break;
      case "--skip-cloud-node-setup":
        args.skipCloudNodeSetup = true;
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
  if (args.phase !== "sysdeploy" && !args.stateFile) {
    throw new Error("--state-file is required");
  }
  return args;
}

function usage() {
  console.log(`Usage:
  node examples/e2e/verify.mjs --phase sysdeploy|setup|schedule|assert|cleanup [options]

Options:
  --gateway <url>          Admin gateway URL. Default: http://127.0.0.1:11000
  --web <url>              Web host URL. Default: http://127.0.0.1:9527
  --host <host>            Public host written to SysDeploy public endpoints.
  --space <space_id>       Space ID. Default: crypto
  --rule <rule_id>         Collector rule ID. Default: binance_spot_kline_1h
  --node <node_id>         Cloud runtime node ID. Default: e2e-scf-node
  --package <package_id>   Code package ID. Default: moox-collector_dev
  --dataset <dataset_id>   Dataset ID. Default: binance_spot_kline_1h
  --e2e-node <node_id>     Admin Gateway node used by SysDeploy checks.
  --state-file <path>      Local state shared by setup/schedule/assert phases.
  --skip-cloud-node-setup  Reuse real deployed CloudNodes; do not create e2e-local.
  --timeout-seconds <n>    Assertion timeout. Default: 120`);
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
  if (args.phase === "sysdeploy" || args.phase === "setup") {
    await setup(args);
    return;
  }
  if (args.phase === "schedule") {
    await schedule(args);
    return;
  }
  if (args.phase === "assert") {
    await assertAfterSCF(args);
    return;
  }
  if (args.phase === "cleanup") {
    await cleanup(args);
    return;
  }
  throw new Error(`unsupported phase: ${args.phase}`);
}

async function setup(args) {
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
  let realSCFNodeIDs = [];
  let realSCFNodes = [];
  if (args.skipCloudNodeSetup) {
    realSCFNodes = await waitFor("online Tencent SCF nodes", 60_000, async () => {
      const rsp = await adminPost(args, token, "cloudnode", "GetNodeList", {
        node_type: "scf-event",
        page: { page: 1, size: 200 },
      }, { spaceId: args.space });
      const eligible = currentRealSCFNodes(rsp.items || [], Date.now(), 120_000);
      assertRealSCFFleet(eligible);
      return eligible;
    });
    realSCFNodeIDs = realSCFNodes.map((node) => node.node_id);
    log(`cloudnode setup skipped: using real SCF nodes ${realSCFNodeIDs.join(",")}`);
  } else {
    await ensureCloudNode(args, token);
  }
  await ensureCollectorRule(args, token);
  await writeState(args, {
    setup_complete: true,
    require_real_scf: args.skipCloudNodeSetup,
    real_scf_node_ids: realSCFNodeIDs,
    real_scf_nodes: realSCFNodes.map((node) => ({
      node_id: node.node_id,
      package_id: node.package_id,
      package_version: node.package_version,
      last_heartbeat: node.last_heartbeat,
      supported_workloads: node.supported_workloads,
    })),
  });
  log("setup ok: queue topology and collector rule are ready");
}

async function cleanup(args) {
  const token = await login(args);
  await disableCollectorRule(args, token);
}

async function disableCollectorRule(args, token) {
  const rsp = await adminPost(args, token, "collectmgr", "DisableTaskRule", {
    space_id: args.space,
    rule_id: args.rule,
  }, { raw: true });
  if (retOK(rsp.ret_info)) {
    log(`disabled collector rule: ${args.rule}`);
    return;
  }
  const message = retMsg(rsp.ret_info);
  if (/not found|不存在/i.test(message)) {
    log(`collector rule was not present: ${args.rule}`);
    return;
  }
  throw new Error(`cleanup collector rule failed: ${message}`);
}

async function schedule(args) {
  const token = await login(args);
  const previousState = await readState(args);
  const storageBefore = await waitFor("storage target Dataset view ready", 60_000, async () =>
    datasetRowSnapshot(args, token));
  const scheduleDelay = scheduleLeadDelay(Date.now(), 60_000, 15_000);
  if (scheduleDelay > 0) {
    log(`waiting ${scheduleDelay}ms for a deterministic future schedule window`);
    await sleep(scheduleDelay);
  }
  await adminPost(args, token, "collectmgr", "ScheduleTasks", { space_id: args.space });

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
    const current = currentScheduledJobItems(instances, rows);
    if (current.length !== instances.length) {
      throw new Error(`cloudnode has ${current.length}/${instances.length} current job items`);
    }
    const notPending = current.filter((item) => !isJobStatus(item.status, "JOB_ITEM_STATUS_PENDING"));
    if (notPending.length > 0) {
      throw new FatalAssertionError(`scheduled job reached a terminal state before execute_at: ${notPending.map((item) => item.job_item_id).join(", ")}`);
    }
    assertFutureExecutionTimes(current, Date.now());
    return current;
  });

  await writeState(args, {
    ...previousState,
    setup_complete: true,
    scheduled_job_ids: jobItems.map((item) => item.job_item_id),
    execute_at: jobItems.map((item) => item.execute_at),
    storage_before: storageBefore.fingerprint,
  });
  await disableCollectorRule(args, token);
  await assertScheduledJobsRemainPendingUntilDue(args, token, instances, jobItems);
  await writeState(args, {
    ...previousState,
    setup_complete: true,
    scheduled_job_ids: jobItems.map((item) => item.job_item_id),
    execute_at: jobItems.map((item) => item.execute_at),
    storage_before: storageBefore.fingerprint,
    pending_until_due: true,
    rule_disabled_after_schedule: true,
  });
  log(`schedule ok: instances=${instances.length}, job_items=${jobItems.length}, pending_until_due=true`);
  logLifecycleQueries(jobItems, "scheduled");
}

async function assertAfterSCF(args) {
  const token = await login(args);
  await updatePublicDeployments(args, token);
  const timeoutMs = args.timeoutSeconds * 1000;
  const state = await readState(args);

  const scheduledIDs = new Set(state.scheduled_job_ids || []);
  if (scheduledIDs.size === 0) {
    throw new FatalAssertionError("E2E state does not contain scheduled job items");
  }
  const jobItems = await waitFor("cloudnode scheduled job items success", timeoutMs, async () => {
    const rows = await listJobItems(args, token);
    const current = rows.filter((item) => scheduledIDs.has(item.job_item_id));
    if (current.length !== scheduledIDs.size) {
      throw new Error(`cloudnode has ${current.length}/${scheduledIDs.size} scheduled job items`);
    }
    const failed = failedJobItems(current);
    if (failed.length > 0) {
      throw new FatalAssertionError(`job item failed: ${failed.map((item) => `${item.job_item_id}:${statusName(item.status, JOB_STATUS)}:${item.last_error_message || ""}`).join(", ")}`);
    }
    const pending = current.filter((item) => !isJobStatus(item.status, "JOB_ITEM_STATUS_SUCCESS"));
    if (pending.length > 0) {
      throw new Error(`${pending.length}/${current.length} scheduled job items are not success yet`);
    }
    return current;
  });
  if (state.require_real_scf) {
    assertRealExecutionNodes(jobItems, state.real_scf_node_ids);
  }

  const instances = await waitFor("collector task instances success", timeoutMs, async () => {
    const rows = await listTaskInstances(args, token);
    if (rows.length === 0) throw new Error("collector task instances are empty");
    const current = currentScheduledTaskInstances(rows, scheduledIDs);
    const currentIDs = new Set(current.map((item) => item.cloud_job_item_id));
    if (current.length !== scheduledIDs.size || currentIDs.size !== scheduledIDs.size) {
      throw new Error(`collector has ${currentIDs.size}/${scheduledIDs.size} task instances bound to this E2E run`);
    }
    const failed = current.filter((item) => isTaskStatus(item.last_exec_status, "TASK_INSTANCE_STATUS_FAILED"));
    if (failed.length > 0) {
      throw new FatalAssertionError(`task instance failed: ${failed.map((item) => `${item.task_id}:${statusName(item.last_exec_status, TASK_STATUS)}`).join(", ")}`);
    }
    const pending = current.filter((item) => !isTaskStatus(item.last_exec_status, "TASK_INSTANCE_STATUS_SUCCESS"));
    if (pending.length > 0) {
      throw new Error(`${pending.length}/${current.length} current task instances are not success yet`);
    }
    return current;
  });

  const immediateJob = await submitImmediateJob(args, token, instances[0]);
  await writeState(args, {
    ...state,
    immediate_job_item_id: immediateJob.job_item_id,
  });
  const immediate = await waitFor("immediate job item success", timeoutMs, async () => {
    const rows = await listJobItems(args, token, immediateJob.job_id);
    const item = rows.find((row) => row.job_item_id === immediateJob.job_item_id);
    if (!item) throw new Error(`immediate job item ${immediateJob.job_item_id} is not visible`);
    if (failedJobItems([item]).length > 0) {
      throw new FatalAssertionError(`immediate job failed: ${item.job_item_id}:${statusName(item.status, JOB_STATUS)}:${item.last_error_message || ""}`);
    }
    if (!isJobStatus(item.status, "JOB_ITEM_STATUS_SUCCESS")) {
      throw new Error(`immediate job ${item.job_item_id} is not success yet`);
    }
    return item;
  });
  if (state.require_real_scf) {
    assertRealExecutionNodes([immediate], state.real_scf_node_ids);
  }
  await writeState(args, {
    ...state,
    immediate_job_item_id: immediate.job_item_id,
  });
  logLifecycleQueries([immediate], "immediate");

  const failureJob = await submitControlledFailureJob(args, token, instances[0]);
  await writeState(args, {
    ...state,
    immediate_job_item_id: immediate.job_item_id,
    failure_job_item_id: failureJob.job_item_id,
  });
  const terminalFailure = await waitFor("controlled failure job item terminal", timeoutMs, async () => {
    const rows = await listJobItems(args, token, failureJob.job_id);
    const item = rows.find((row) => row.job_item_id === failureJob.job_item_id);
    if (!item) throw new Error(`controlled failure job item ${failureJob.job_item_id} is not visible`);
    if (isJobStatus(item.status, "JOB_ITEM_STATUS_SUCCESS")) {
      throw new FatalAssertionError(`controlled failure unexpectedly succeeded: ${item.job_item_id}`);
    }
    if (!isJobStatus(item.status, "JOB_ITEM_STATUS_FAILED")) {
      throw new Error(`controlled failure job ${item.job_item_id} is not terminal yet`);
    }
    if (item.last_error_code !== "COLLECT_FAILED") {
      throw new FatalAssertionError(`controlled failure error_code=${item.last_error_code || "missing"}, want COLLECT_FAILED`);
    }
    const errorMessage = String(item.last_error_message || "");
    if (!errorMessage || errorMessage.length > 1024 || !/HTTP[^0-9]*400|400/.test(errorMessage)) {
      throw new FatalAssertionError(`controlled failure has unexpected error message: ${errorMessage.slice(0, 160) || "missing"}`);
    }
    if (statusName(item.last_error_kind, JOB_ERROR_KIND) !== "JOB_ITEM_ERROR_KIND_RETRYABLE") {
      throw new FatalAssertionError(`controlled failure error_kind=${statusName(item.last_error_kind, JOB_ERROR_KIND) || "missing"}, want retryable`);
    }
    return item;
  });
  if (state.require_real_scf) {
    assertRealExecutionNodes([terminalFailure], state.real_scf_node_ids);
  }

  const batchJobs = await submitImmediateJobBatch(args, token, instances, BATCH_ACCEPTANCE_COUNT);
  const batchJobIDs = new Set(batchJobs.map((item) => item.job_item_id));
  const batchResults = await waitFor("immediate batch job items success", timeoutMs, async () => {
    const rows = await listJobItems(args, token, batchJobs[0].job_id);
    const current = rows.filter((item) => batchJobIDs.has(item.job_item_id));
    if (current.length !== batchJobIDs.size) {
      throw new Error(`cloudnode has ${current.length}/${batchJobIDs.size} immediate batch job items`);
    }
    const failed = failedJobItems(current);
    if (failed.length > 0) {
      throw new FatalAssertionError(`batch job failed: ${failed.map((item) => `${item.job_item_id}:${statusName(item.status, JOB_STATUS)}:${item.last_error_message || ""}`).join(", ")}`);
    }
    const pending = current.filter((item) => !isJobStatus(item.status, "JOB_ITEM_STATUS_SUCCESS"));
    if (pending.length > 0) {
      throw new Error(`${pending.length}/${current.length} immediate batch jobs are not success yet`);
    }
    return current;
  });
  if (state.require_real_scf) {
    assertRealExecutionNodes(batchResults, state.real_scf_node_ids);
  }
  assertTaskInstanceWriteEvidence(instances, jobItems);
  const writeEvidence = collectStorageWriteEvidence([
    ...jobItems,
    immediate,
    ...batchResults,
  ]);
  await writeState(args, {
    ...state,
    immediate_job_item_id: immediate.job_item_id,
    failure_job_item_id: terminalFailure.job_item_id,
    failure_status: statusName(terminalFailure.status, JOB_STATUS),
    failure_error_code: terminalFailure.last_error_code,
    batch_job_item_ids: batchResults.map((item) => item.job_item_id),
    expected_batch_size: EXPECTED_BATCH_SIZE,
  });
  logLifecycleQueries(batchResults, "batch");
  logLifecycleQueries([terminalFailure], "failure");

  if (writeEvidence.writtenKeys.length > 0) {
    await waitFor("Storage Primary exact written RowKeys", timeoutMs, async () => {
      await assertWrittenRowKeysReadable(args, token, writeEvidence.writtenKeys);
      return true;
    });
    log(`storage write evidence: rows_written=${writeEvidence.rowsWritten}, verified_row_keys=${writeEvidence.writtenKeys.length}`);
  } else {
    log("storage write evidence: every successful JobItem reported no new closed K-line");
  }
  const storageAfter = await waitFor("storage target Dataset available after write", timeoutMs, async () => {
    const snapshot = await datasetRowSnapshot(args, token);
    if (snapshot.rows.length === 0) {
      throw new Error("storage has no time-series rows for binance spot kline dataset yet");
    }
    return snapshot;
  });
  const rows = storageAfter.rows;

  const sample = rows[0]?.key || {};
  const sampleTime = sample.data_time || new Date().toISOString();
  const queryRows = await waitFor("storage view rows", timeoutMs, async () => {
    const end = new Date(sampleTime);
    if (Number.isNaN(end.getTime())) throw new Error(`invalid sample data_time: ${sampleTime}`);
    const start = new Date(end.getTime() - 60 * 60 * 1000);
    const rsp = await storagePost(args, token, "view", "QueryTimeSeriesRows", {
      space_id: args.space,
      view_id: "binance_spot_1h_view",
      keys: [{ space_id: args.space, subject_id: sample.subject_id, freq: sample.freq }],
      time_range: { start_time: start.toISOString(), end_time: end.toISOString() },
      page: { page: 1, size: 20 },
    });
    if (rsp.complete !== true) throw new Error("storage view response is not complete");
    if ((rsp.rows || []).length === 0) throw new Error("storage view has no rows for the sampled fact");
    return rsp.rows;
  });
  log(`assert ok: scheduled_job_items=${jobItems.length}, batch_job_items=${batchResults.length}, expected_batch_size=${EXPECTED_BATCH_SIZE}, immediate_job_item=${immediate.job_item_id}, failure_job_item=${terminalFailure.job_item_id}, instances=${instances.length}, storage_rows=${rows.length}, view_rows=${queryRows.length}, target=${args.space}/${args.dataset}, sample=${sample.subject_id || "-"}:${sample.freq || "-"}:${sample.data_time || "-"}`);
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
    username: args.username,
  });
  const encrypted = encryptPassword(args.password, saltRsp.salt, saltRsp.timestamp);
  const loginRsp = await adminPost(args, "", "auth", "Login", {
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
  if (!loginRsp.request_signing_key || !loginRsp.session_id) {
    throw new Error("login response missing request signing session");
  }
  return { accessToken: loginRsp.access_token, signingKey: loginRsp.request_signing_key };
}

function signRequest(signingKey, method, path, body, headers) {
  const timestamp = Math.floor(Date.now() / 1000);
  const nonce = randomBytes(32).toString("hex");
  const signedValues = [
    ["x-app-id", headers["X-App-Id"] || ""],
    ["x-app-key", headers["X-App-Key"] || ""],
    ["x-space-id", headers["X-Space-Id"] || ""],
  ];
  const hasSignedHeaders = signedValues.some(([, value]) => value !== "");
  const canonical = ["moox-request-v1", method.toUpperCase(), path];
  if (hasSignedHeaders) {
    for (const [name, value] of signedValues) canonical.push(`${name}:${value}`);
  }
  canonical.push(createHash("sha256").update(body).digest("hex"), String(timestamp), nonce);
  const signature = createHmac("sha256", signingKey).update(canonical.join("\n")).digest("hex");
  return { timestamp: String(timestamp), nonce, signature };
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
      node_id: item.node_id,
      deployment: next,
    });
  }
  log(`public service deployments host=${args.host}, updated=${updates.length}`);
}

function deploymentInput(item) {
  return {
    node_id: item.node_id,
    service_name: item.service_name,
    service_kind: item.service_kind,
    protocol: item.protocol,
    host: item.host,
    port: item.port,
    gateway_path: item.gateway_path || "",
    gateway_service_id: item.gateway_service_id || "",
    gateway_enabled: item.gateway_enabled === true,
    scope: item.scope,
    status: item.status,
    description: item.description || "",
    extra_config: item.extra_config || "{}",
  };
}

async function assertSysDeploySingleInstanceContract(args, token) {
  const serviceName = `e2e_sysdeploy_${randomBytes(8).toString("hex")}`;
  const first = {
    node_id: args.e2eNodeId,
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
      node_id: args.e2eNodeId,
      service_name: serviceName,
      deployment: update,
    });
    const got = await adminPost(args, token, "sysdeploy", "GetServiceDeployment", { node_id: args.e2eNodeId, service_name: serviceName });
    if (got.deployment?.base_url !== "http://127.0.0.2:19091" || got.deployment?.rpc_address !== "127.0.0.2:19091") {
      throw new Error(`sysdeploy returned stale derived address: ${JSON.stringify(got.deployment)}`);
    }

    await adminPost(args, token, "sysdeploy", "DeleteServiceDeployment", { node_id: args.e2eNodeId, service_name: serviceName });
    await adminPost(args, token, "sysdeploy", "CreateServiceDeployment", { deployment: first });
    await adminPost(args, token, "sysdeploy", "DeleteServiceDeployment", { node_id: args.e2eNodeId, service_name: serviceName });

    const active = await adminPost(args, token, "sysdeploy", "ListActiveServiceDeployments", { node_id: args.e2eNodeId });
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
    node_id: args.e2eNodeId,
    service_name: serviceName,
    page: { page: 1, size: 10 },
  });
  if ((listed.deployments || []).some((item) => item.service_name === serviceName)) {
    await adminPost(args, token, "sysdeploy", "DeleteServiceDeployment", { node_id: args.e2eNodeId, service_name: serviceName });
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

  const active = await adminPost(args, token, "sysdeploy", "ListActiveServiceDeployments", { node_id: args.e2eNodeId });
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
  const submitted = await adminPost(args, token, "cloudnode", "SubmitCreateNodes", {
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
        supported_workloads: ["collect.binance.kline", "collect.binance.symbol"],
      },
    }],
  }, { spaceId: args.space });
  if (!submitted.job_id || Number(submitted.total_count || 0) !== 1) {
    throw new Error("SubmitCreateNodes did not return one asynchronous node item");
  }
  await waitFor("local CloudNode publish job", args.timeoutSeconds * 1000, async () => {
    const status = await adminPost(args, token, "cloudnode", "GetNodeBatchChange", {
      job_id: submitted.job_id,
    }, { spaceId: args.space });
    const jobStatus = status.job?.status;
    if (jobStatus === "NODE_BATCH_STATUS_FAILED" || jobStatus === "NODE_BATCH_STATUS_PARTIAL") {
      const failed = (status.items || []).find((item) => item.status === "NODE_BATCH_ITEM_STATUS_FAILED");
      throw new FatalAssertionError(`local CloudNode publish failed: ${failed?.error_message || jobStatus}`);
    }
    if (jobStatus !== "NODE_BATCH_STATUS_SUCCESS") {
      throw new Error(`local CloudNode publish status=${jobStatus || ""}`);
    }
    return status;
  });
  log(`cloudnode runtime node ready: ${args.node}`);
}

async function ensureCollectorRule(args, token) {
  const rule = {
    space_id: args.space,
    rule_id: args.rule,
    data_type: "kline",
    exchange: "binance",
    collect_params: {
      source: { kind: "dataset_subjects", dataset_id: "binance_spot_symbols" },
      collector: { exchange: "binance", market: "spot", data_type: "kline", intervals: ["1h"] },
      target: { dataset_id: args.dataset },
      schedule: { interval: "1m" },
    },
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

async function listJobItems(args, token, jobId = args.rule) {
  const rsp = await adminPost(args, token, "cloudnode", "ListJobItems", {
    space_id: args.space,
    job_id: jobId,
    page: { page: 1, size: 200 },
  }, { spaceId: args.space });
  return rsp.items || [];
}

async function datasetRowSnapshot(args, token) {
  const rsp = await storagePost(args, token, "view", "QueryTimeSeriesRows",
    datasetSnapshotRequest(args));
  const rows = rsp.rows || [];
  return {
    rows,
    fingerprint: JSON.stringify(stableJSON({
      rows,
      total: rsp.page_result?.total,
      total_state: rsp.page_result?.total_state,
    })),
  };
}

export function datasetSnapshotRequest(args) {
  return {
    space_id: args.space,
    view_id: "binance_spot_1h_view",
    time_range: { start_time: "1970-01-01T00:00:00Z" },
    sorts: [{ field_name: "data_time", desc: true }],
    page: { page: 1, size: 200 },
  };
}

function stableJSON(value) {
  if (Array.isArray(value)) {
    return value.map(stableJSON).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, stableJSON(value[key])]));
  }
  return value;
}

export function collectStorageWriteEvidence(jobItems) {
  let rowsWritten = 0;
  const writtenKeys = [];
  for (const item of jobItems || []) {
    const tasks = item.result_summary?.tasks;
    if (!Array.isArray(tasks) || tasks.length === 0) {
      throw new FatalAssertionError(`successful JobItem ${item.job_item_id || "unknown"} does not contain collection write evidence`);
    }
    const expectedIntervals = Array.isArray(item.params?.intervals) && item.params.intervals.length > 0
      ? item.params.intervals.map(String)
      : [String(item.params?.interval || "")];
    const actualIntervals = tasks.map((task) => String(task.interval || ""));
    if (JSON.stringify([...actualIntervals].sort()) !== JSON.stringify([...expectedIntervals].sort())) {
      throw new FatalAssertionError(`JobItem ${item.job_item_id || "unknown"} task evidence does not match requested intervals`);
    }
    for (const task of tasks) {
      const expectedDataType = String(item.params?.data_type || "");
      const expectedSymbol = String(item.params?.symbol || "");
      if (String(task.data_type || "") !== expectedDataType || String(task.symbol || "") !== expectedSymbol) {
        throw new FatalAssertionError(`JobItem ${item.job_item_id || "unknown"} task evidence does not match requested data_type/symbol`);
      }
      const expectedScope = {
        space_id: String(item.params?.space_id || item.space_id || ""),
        dataset_id: String(item.params?.dataset_id || ""),
        subject_id: String(item.params?.subject_id || expectedSymbol),
        freq: expectedKlineFreq(task.interval),
      };
      if (JSON.stringify(stableJSON(task.storage_read_scope || {})) !== JSON.stringify(stableJSON(expectedScope))) {
        throw new FatalAssertionError(`JobItem ${item.job_item_id || "unknown"} Storage read scope does not match its request`);
      }
      const count = Number(task.rows_written);
      if (!Number.isInteger(count) || count < 0) {
        throw new FatalAssertionError(`invalid rows_written in JobItem ${item.job_item_id || "unknown"}: ${task.rows_written}`);
      }
      rowsWritten += count;
      if (count === 0 && task.zero_write_reason !== "no_new_closed_kline") {
        throw new FatalAssertionError(`zero-write JobItem ${item.job_item_id || "unknown"} is missing an accepted reason`);
      }
      if (count > 0) {
        const samples = task.written_row_key_samples || [];
        if (samples.length === 0) {
          throw new FatalAssertionError(`positive JobItem ${item.job_item_id || "unknown"} does not contain written RowKey samples`);
        }
        for (const key of samples) {
          const scope = {
            space_id: key.space_id,
            dataset_id: key.dataset_id,
            subject_id: key.subject_id,
            freq: key.freq,
          };
          if (!key.data_time || JSON.stringify(stableJSON(scope)) !== JSON.stringify(stableJSON(expectedScope))) {
            throw new FatalAssertionError(`JobItem ${item.job_item_id || "unknown"} contains an out-of-scope written RowKey`);
          }
        }
        writtenKeys.push(...samples);
      }
    }
  }
  return { rowsWritten, writtenKeys };
}

function expectedKlineFreq(interval) {
  const value = String(interval || "");
  if (value.length < 2) return value;
  const unit = value.at(-1);
  if (unit === "h" || unit === "H") return `${value.slice(0, -1)}H`;
  if (unit === "d" || unit === "D") return `${value.slice(0, -1)}D`;
  if (unit === "w" || unit === "W") return `${value.slice(0, -1)}W`;
  if (unit === "y" || unit === "Y") return `${value.slice(0, -1)}Y`;
  return value;
}

export function assertTaskInstanceWriteEvidence(instances, jobItems) {
  const byID = new Map((jobItems || []).map((item) => [item.job_item_id, item]));
  for (const instance of instances || []) {
    const item = byID.get(instance.cloud_job_item_id);
    if (!item) {
      throw new FatalAssertionError(`TaskInstance ${instance.task_id || "unknown"} has no matching JobItem`);
    }
    const instanceTasks = instance.result?.tasks;
    const jobTasks = item.result_summary?.tasks;
    if (!Array.isArray(instanceTasks) || JSON.stringify(stableJSON(instanceTasks)) !== JSON.stringify(stableJSON(jobTasks))) {
      throw new FatalAssertionError(`TaskInstance ${instance.task_id || "unknown"} did not persist its JobItem write evidence`);
    }
  }
}

async function assertWrittenRowKeysReadable(args, token, keys) {
  const unique = [...new Map(keys.map((key) => [JSON.stringify(stableJSON(key)), key])).values()];
  for (let offset = 0; offset < unique.length; offset += 50) {
    const batch = unique.slice(offset, offset + 50);
    const rsp = await storagePost(args, token, "access", "ReadTimeSeriesRows",
      primaryWrittenRowsRequest(args, batch));
    const visible = primaryRowsWithField(rsp.rows || [], "close");
    const missing = batch.filter((key) => !visible.has(JSON.stringify(stableJSON(key))));
    if (missing.length > 0) {
      throw new Error(`${missing.length}/${batch.length} written RowKeys are not readable from Storage Primary`);
    }
  }
}

export function primaryRowsWithField(rows, fieldID) {
  return new Set((rows || [])
    .filter((row) => (row.fields || []).some((field) => {
      if (field.field_id !== fieldID || !field.value || typeof field.value !== "object") return false;
      return Object.entries(field.value).some(([name, value]) =>
        name.endsWith("_value") && value !== null && value !== undefined);
    }))
    .map((row) => JSON.stringify(stableJSON(row.key || {}))));
}

export function primaryWrittenRowsRequest(args, keys) {
  return {
    space_id: args.space,
    dataset_id: args.dataset,
    keys,
    column_names: ["close"],
    page: { page: 1, size: keys.length },
  };
}

async function writeState(args, value) {
  await writeFile(args.stateFile, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
}

async function readState(args) {
  try {
    return JSON.parse(await readFile(args.stateFile, "utf8"));
  } catch (err) {
    throw new FatalAssertionError(`cannot read E2E state: ${err.message}`);
  }
}

async function submitImmediateJob(args, token, instance) {
  if (!instance) throw new FatalAssertionError("cannot submit immediate job without a task instance");
  const suffix = `${Date.now()}-${randomBytes(4).toString("hex")}`;
  const diagnosticTaskID = `e2e-immediate-${suffix}`;
  const item = {
    space_id: args.space,
    job_id: `${args.rule}-immediate-e2e`,
    job_item_id: `e2e-immediate-${suffix}`,
    job_type: `collect.${instance.exchange || "binance"}.${instance.data_type || "kline"}`,
    params: {
      ...(instance.task_params || {}),
      space_id: args.space,
      task_id: diagnosticTaskID,
      dataset_id: instance.dataset_id || args.dataset,
      data_type: instance.data_type || "kline",
      exchange: instance.exchange || "binance",
      market: instance.market || "spot",
      subject_id: instance.subject_id,
      symbol: instance.symbol,
      interval: instance.interval || "1h",
    },
    priority: 100,
  };
  const rsp = await adminPost(args, token, "cloudnode", "SubmitJobItems", {
    items: [item],
  }, { spaceId: args.space });
  const ack = (rsp.acks || []).find((candidate) => candidate.job_item_id === item.job_item_id);
  const ackStatus = statusName(ack?.status, {
    1: "JOB_ITEM_ACK_STATUS_CREATED",
    2: "JOB_ITEM_ACK_STATUS_DEDUPLICATED",
    3: "JOB_ITEM_ACK_STATUS_REJECTED",
  });
  if (!ack || !["JOB_ITEM_ACK_STATUS_CREATED", "JOB_ITEM_ACK_STATUS_DEDUPLICATED"].includes(ackStatus)) {
    throw new FatalAssertionError(`immediate job rejected: ${ack?.reject_reason || ackStatus || "missing ack"}`);
  }
  log(`submitted immediate job without execute_at: ${item.job_item_id}; task_instance_update=not_expected`);
  return item;
}

async function submitImmediateJobBatch(args, token, instances, count) {
  if (!Array.isArray(instances) || instances.length === 0) {
    throw new FatalAssertionError("cannot submit immediate batch without task instances");
  }
  const suffix = `${Date.now()}-${randomBytes(4).toString("hex")}`;
  const jobID = `${args.rule}-batch-e2e-${suffix}`;
  const items = Array.from({ length: count }, (_, index) => {
    const instance = instances[index % instances.length];
    const jobItemID = `e2e-batch-${suffix}-${String(index).padStart(3, "0")}`;
    return {
      space_id: args.space,
      job_id: jobID,
      job_item_id: jobItemID,
      job_type: `collect.${instance.exchange || "binance"}.${instance.data_type || "kline"}`,
      params: {
        ...(instance.task_params || {}),
        space_id: args.space,
        task_id: jobItemID,
        dataset_id: instance.dataset_id || args.dataset,
        data_type: instance.data_type || "kline",
        exchange: instance.exchange || "binance",
        market: instance.market || "spot",
        subject_id: instance.subject_id,
        symbol: instance.symbol,
        interval: instance.interval || "1h",
      },
      priority: 100,
    };
  });
  const rsp = await adminPost(args, token, "cloudnode", "SubmitJobItems", {
    items,
  }, { spaceId: args.space });
  const ackStatuses = {
    1: "JOB_ITEM_ACK_STATUS_CREATED",
    2: "JOB_ITEM_ACK_STATUS_DEDUPLICATED",
    3: "JOB_ITEM_ACK_STATUS_REJECTED",
  };
  const accepted = new Set((rsp.acks || [])
    .filter((ack) => ["JOB_ITEM_ACK_STATUS_CREATED", "JOB_ITEM_ACK_STATUS_DEDUPLICATED"].includes(statusName(ack.status, ackStatuses)))
    .map((ack) => ack.job_item_id));
  const rejected = items.filter((item) => !accepted.has(item.job_item_id));
  if (rejected.length > 0) {
    throw new FatalAssertionError(`immediate batch rejected ${rejected.length}/${items.length}: ${rejected.map((item) => item.job_item_id).join(", ")}`);
  }
  log(`submitted immediate batch without execute_at: count=${items.length}, expected_fetch_batch_size=${EXPECTED_BATCH_SIZE}`);
  return items;
}

export function controlledFailureJob(args, instance, suffix) {
  const id = `e2e-failure-${suffix}`;
  return {
    space_id: args.space,
    job_id: `${args.rule}-failure-e2e`,
    job_item_id: id,
    job_type: "collect.binance.kline",
    params: {
      space_id: args.space,
      task_id: id,
      dataset_id: instance?.dataset_id || args.dataset,
      data_type: "kline",
      exchange: "binance",
      market: "spot",
      subject_id: "INVALID-E2E-SYMBOL",
      symbol: "INVALID-E2E-SYMBOL",
      interval: "1h",
    },
    priority: 100,
  };
}

async function submitControlledFailureJob(args, token, instance) {
  const suffix = `${Date.now()}-${randomBytes(4).toString("hex")}`;
  const item = controlledFailureJob(args, instance, suffix);
  const rsp = await adminPost(args, token, "cloudnode", "SubmitJobItems", {
    items: [item],
  }, { spaceId: args.space });
  const ack = (rsp.acks || []).find((candidate) => candidate.job_item_id === item.job_item_id);
  const ackStatus = statusName(ack?.status, {
    1: "JOB_ITEM_ACK_STATUS_CREATED",
    2: "JOB_ITEM_ACK_STATUS_DEDUPLICATED",
    3: "JOB_ITEM_ACK_STATUS_REJECTED",
  });
  if (!ack || !["JOB_ITEM_ACK_STATUS_CREATED", "JOB_ITEM_ACK_STATUS_DEDUPLICATED"].includes(ackStatus)) {
    throw new FatalAssertionError(`controlled failure job rejected: ${ack?.reject_reason || ackStatus || "missing ack"}`);
  }
  log(`submitted controlled failure job: ${item.job_item_id}; expected_terminal=JOB_ITEM_STATUS_FAILED`);
  return item;
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
  const accessToken = typeof token === "string" ? token : token?.accessToken;
  if (accessToken) {
    headers.Authorization = accessToken;
    headers["X-Access-Token"] = accessToken;
  }
  if (options.spaceId) {
    headers["X-Space-Id"] = options.spaceId;
  }
  const url = args.phase === "sysdeploy" && service === "sysdeploy"
    ? `http://127.0.0.1:11109/trpc.moox.ops.SysDeploy/${method}`
    : `${args.gateway}/api/admin/${service === "storage" ? "storage" : service}/${method}`;
  const payload = JSON.stringify(body || {});
  if (token?.signingKey && !url.includes(":11109/")) {
    const signed = signRequest(token.signingKey, "POST", new URL(url).pathname, Buffer.from(payload), headers);
    headers["X-Moox-Timestamp"] = signed.timestamp;
    headers["X-Moox-Nonce"] = signed.nonce;
    headers["X-Moox-Signature"] = signed.signature;
  }
  const rsp = await fetchJSON(url, {
    method: "POST",
    headers,
    body: payload,
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

export function statusName(value, table) {
  if (typeof value === "number") return table[value] || String(value);
  if (typeof value === "string" && /^\d+$/.test(value)) return table[Number(value)] || value;
  return String(value || "");
}

function isJobStatus(value, expected) {
  return statusName(value, JOB_STATUS) === expected;
}

export function failedJobItems(rows) {
  return rows.filter((item) =>
    isJobStatus(item.status, "JOB_ITEM_STATUS_FAILED") ||
    isJobStatus(item.status, "JOB_ITEM_STATUS_ENQUEUE_FAILED"));
}

export function currentScheduledJobItems(instances, jobItems) {
  const ids = new Set(instances.map((item) => item.cloud_job_item_id).filter(Boolean));
  return jobItems.filter((item) => ids.has(item.job_item_id));
}

export function currentScheduledTaskInstances(instances, scheduledIDs) {
  const ids = scheduledIDs instanceof Set ? scheduledIDs : new Set(scheduledIDs || []);
  return instances.filter((item) => ids.has(item.cloud_job_item_id));
}

export function assertFutureExecutionTimes(items, nowMs) {
  for (const item of items) {
    const executeAt = Date.parse(item.execute_at || "");
    if (!Number.isFinite(executeAt) || executeAt <= nowMs) {
      throw new FatalAssertionError(`scheduled job ${item.job_item_id || "-"} execute_at is not in the future: ${item.execute_at || "missing"}`);
    }
  }
}

export function currentRealSCFNodes(nodes, nowMs, maxAgeMs, requiredJobType = "collect.binance.kline") {
  return nodes.filter((node) => {
    if (node.provider !== "tencent-scf" || node.node_type !== "scf-event") return false;
    if (statusName(node.status, NODE_STATUS) !== "NODE_STATUS_ONLINE") return false;
    if (!(node.supported_workloads || []).includes(requiredJobType)) return false;
    const heartbeat = Date.parse(node.last_heartbeat || "");
    return Number.isFinite(heartbeat) && heartbeat <= nowMs && nowMs - heartbeat <= maxAgeMs;
  });
}

export function assertRealSCFFleet(nodes) {
  const packageIDs = new Set(nodes.map((node) => node.package_id).filter(Boolean));
  if (nodes.length < 2 || packageIDs.size < 2) {
    throw new Error(`need two online tencent-scf nodes from different packages; nodes=${nodes.length} packages=${packageIDs.size}`);
  }
}

export function assertRealExecutionNodes(items, realNodeIDs) {
  const allowed = new Set(realNodeIDs || []);
  if (allowed.size === 0) {
    throw new FatalAssertionError("E2E state does not contain verified Tencent SCF nodes");
  }
  const invalid = items.filter((item) => !item.execution_node || !allowed.has(item.execution_node));
  if (invalid.length > 0) {
    throw new FatalAssertionError(`job items were not executed by verified Tencent SCF nodes: ${invalid.map((item) => `${item.job_item_id || "-"}:${item.execution_node || "missing"}`).join(", ")}`);
  }
}

async function assertScheduledJobsRemainPendingUntilDue(args, token, instances, jobItems) {
  const dueAt = Math.min(...jobItems.map((item) => Date.parse(item.execute_at || "")));
  const guardUntil = dueAt - 100;
  while (Date.now() < guardUntil) {
    const rows = await listJobItems(args, token);
    const current = currentScheduledJobItems(instances, rows);
    if (current.length !== jobItems.length) {
      throw new Error(`cloudnode has ${current.length}/${jobItems.length} scheduled job items during pre-due guard`);
    }
    const executed = current.filter((item) => !isJobStatus(item.status, "JOB_ITEM_STATUS_PENDING"));
    if (executed.length > 0) {
      throw new FatalAssertionError(`scheduled job completed before execute_at: ${executed.map((item) => item.job_item_id).join(", ")}`);
    }
    const currentInstances = currentScheduledTaskInstances(await listTaskInstances(args, token), new Set(current.map((item) => item.job_item_id)));
    const completed = currentInstances.filter((item) => !isTaskStatus(item.last_exec_status, "TASK_INSTANCE_STATUS_PENDING"));
    if (completed.length > 0) {
      throw new FatalAssertionError(`task instance completed before execute_at: ${completed.map((item) => item.task_id).join(", ")}`);
    }
    await sleep(Math.min(250, Math.max(1, guardUntil - Date.now())));
  }
}

export function scheduleLeadDelay(nowMs, intervalMs, minimumLeadMs) {
  const remaining = intervalMs - (nowMs % intervalMs);
  return remaining >= minimumLeadMs ? 0 : remaining + 25;
}

function logLifecycleQueries(items, kind) {
  for (const item of items) {
    const expected = kind === "scheduled"
      ? "received,deferred,delivery_action(RETRY),received,started,instance_reported,done,cloudnode_reported,delivery_action(ACK)"
      : kind === "failure"
        ? "received,started,instance_reported,done,delivery_action(RETRY),received,started,instance_reported,done,delivery_action(RETRY),received,started,instance_reported,done,cloudnode_reported,delivery_action(TERM)"
        : "received,started,instance_reported(stale_task_binding),done,cloudnode_reported,delivery_action(ACK)";
    const note = kind === "immediate" ? " task_instance_update=not_expected" : "";
    log(`CLS query: job_item_id="${item.job_item_id}" kind=${kind} expected_events=${expected}${note}`);
  }
}

function isTaskStatus(value, expected) {
  return statusName(value, TASK_STATUS) === expected;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => {
    console.error(`[e2e] ERROR: ${err.message}`);
    process.exit(1);
  });
}
