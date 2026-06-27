---
name: debug
description: Use when diagnosing MooX end-to-end failures involving collector SCF build/package/publish, Tencent COS or SCF creation, CLS log analysis, remote MooX deployment, control-plane callbacks, heartbeat/task dispatch, or storage write verification.
---

# MooX Debug

## Overview

Use this skill for MooX production-like debugging that crosses local code, remote services, Tencent Cloud SCF/COS/CLS, and storage verification. Keep the investigation evidence-driven: identify the failing boundary before changing code or redeploying.

## First Moves

1. State the concrete symptom, impacted function/node/task ID, expected behavior, and current time window.
2. Check the local repo status and avoid touching unrelated user changes.
3. Identify the path being tested: control plane, storage, collector package, SCF runtime, Tencent Cloud account, remote host, or frontend proxy.
4. Read the detailed workflow in [SCF E2E Debug](references/scf-e2e-debug.md) when the issue involves SCF packaging, publishing, CLS logs, remote deployment, or K-line write verification.

## Safety Rules

- Do not print Tencent SecretKey, service access secret, SSH password, or signed headers in final answers.
- Prefer `moox-cli` commands and bundled MooX scripts over manually repeating fragile Tencent API calls.
- For destructive operations, confirm the target resource name, region, namespace, account, and package version before acting.
- Treat `/data-collector` as old reference only; new collector logic lives under `modules/collector`.
- For frontend requests, management APIs must go through `/api/admin`; service-to-service callbacks should use `/api/service` with service auth.

## Boundary Checklist

Use this order unless evidence points elsewhere:

1. Local build: binary/package generation succeeds and embeds the intended config.
2. Package upload: COS object exists, region/bucket/key match the publish request.
3. SCF creation/update: function name, namespace, region, runtime, handler, environment, and code source match the package.
4. Keepalive: control plane invokes SCF and records heartbeat online.
5. Task dispatch: heartbeat response includes the expected `task_id`, `symbol`, `interval`, and `task_params`.
6. Execution: SCF logs show due-task evaluation and collector execution.
7. Callback: SCF reports task status to `/api/service/collectmgr/ReportTaskStatus`.
8. Storage write: records appear in the target space, subject, dataset, freq, and view.

## Evidence To Preserve

- `git status --short` before edits.
- Build/package command and resulting package path/version.
- COS bucket, region, object key, and package ETag/version when available.
- SCF function name, namespace, region, runtime, handler, environment variables.
- CLS topic ID, query time range, request ID, task ID, and key log lines.
- Control-plane logs around keepalive, task planner, heartbeat, status report, and storage callbacks.
- Storage query parameters and result counts, not full secrets or large payloads.

## Common Mistakes

- Assuming a successful keepalive means task execution happened; verify task dispatch and due-task logs separately.
- Rebuilding collector code from the old `/data-collector` repository instead of `modules/collector`.
- Looking only at DB task instances while the authoritative dispatch path is the in-memory task store after planner recalculation.
- Treating an empty task list as initialization forever; after planner has completed, an empty list should clear downstream caches.
- Debugging frontend 404s against service paths when the frontend must use `/api/admin`.
