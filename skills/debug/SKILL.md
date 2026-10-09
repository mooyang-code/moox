---
name: debug
description: Use when diagnosing MooX end-to-end failures involving collector SCF build/package/publish, Tencent COS or SCF creation, Timer triggers and batch claims, CLS log analysis, remote MooX deployment, or storage write verification.
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
- Treat old standalone collector repository paths as historical only; current collector code and SCF package build logic live under `modules/collector`.
- For frontend requests, management APIs must go through `/api/admin`; service-to-service callbacks should use `/api/service` with service auth.

## Boundary Checklist

Use this order unless evidence points elsewhere:

1. Local build: the SCF package contains `main`, `sources/`, the EventBus CA and (for stockcn) market assets.
2. Package upload: COS object exists and region/bucket/key match the publish request.
3. SCF function: name, namespace, region, runtime, handler and environment match the CloudNode node.
4. Timer: the trigger is enabled with the expected cron (`collector function timer-inventory`).
5. Claim: the function claims its Timer batch through `ClaimTimerBatch`.
6. Execution: CLS logs show per-subject provider results.
7. Storage: `EnsureDatasetPeriod`/`CommitTimeSeriesBatch` succeed through access, and the function publishes `MarketFetchBatchCompleted`.
8. Result: rows appear in the task's result View for the space, subject and frequency.

## Evidence To Preserve

- `git status --short` before edits.
- Build/package command and resulting package path/version.
- COS bucket, region, object key, and package version.
- SCF function name, namespace, region, runtime, handler, Timer cron and environment keys (not values).
- CLS topic ID, query time range, request ID, batch ID and key log lines.
- Collector logs around `ClaimTimerBatch`, completion handling and retries.
- Storage query parameters and result counts, not full secrets or large payloads.

## Common Mistakes

- Assuming a Timer fired because the function exists; check the Timer state and the claimed batch.
- Rebuilding collector code from an old standalone checkout instead of `modules/collector`.
- Looking only at Storage rows without checking the period state (`complete`/`degraded`) and the completion consumer.
- Debugging frontend 404s against service paths when the frontend must use `/api/admin`.
