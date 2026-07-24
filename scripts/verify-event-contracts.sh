#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

production=(--glob '*.go' --glob '*.proto' --glob '*.yaml' --glob '!**/*_test.go' --glob '!docs/**' --glob '!outputs/**')

if rg -n 'MooxMessage|packages/messagepb|messagepb|moox_message|messagepb\.MessageKind' "${production[@]}" .; then
  echo "legacy MooxMessage symbols remain in production sources" >&2
  exit 1
fi
if rg -n 'wrapperspb\.BytesValue|google\.protobuf\.BytesValue' --glob '*.go' --glob '*.proto' --glob '!**/*_test.go' .; then
  echo "legacy BytesValue event wrappers remain" >&2
  exit 1
fi
if rg -n 'c_topic' modules/trade/schema/bus.sql modules/strategy/schema/strategy.sql; then
  echo "trade or strategy outbox still persists c_topic" >&2
  exit 1
fi
if rg -n 'c_payload' modules/trade/schema/bus.sql modules/strategy/schema/strategy.sql; then
  echo "trade or strategy outbox still persists c_payload" >&2
  exit 1
fi
if rg -n 'PublishRaw\(|Client\.Publish\(|FetchRaw|AckToken|NakToken' modules --glob '*.go' --glob '!**/*_test.go'; then
  echo "business modules still use the raw or legacy JetStream event API" >&2
  exit 1
fi
if rg -n 'moox\.cloudnode\.job\.requested|moox\.metrics\.reported|(^|["/])message\.rejected(["/]|$)' modules packages --glob '*.go' --glob '*.proto' --glob '*.yaml' --glob '!**/*_test.go'; then
  echo "legacy event subject/name remains" >&2
  exit 1
fi

(cd packages/events && go test ./...)
echo "event contract verification passed"
