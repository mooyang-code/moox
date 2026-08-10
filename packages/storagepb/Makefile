PROTOC ?= protoc
PROTOBUF_PROTO_DIR ?= $(shell go env GOMODCACHE)/github.com/gogo/protobuf@v1.3.2/protobuf

.PHONY: generate
generate:
	@test -f "$(PROTOBUF_PROTO_DIR)/google/protobuf/timestamp.proto" || (echo "missing google/protobuf/timestamp.proto; run go mod download github.com/gogo/protobuf@v1.3.2" >&2; exit 1)
	$(PROTOC) --proto_path=. --proto_path=$(PROTOBUF_PROTO_DIR) --go_out=. --go_opt=paths=source_relative --go_opt=Mgoogle/protobuf/timestamp.proto=google.golang.org/protobuf/types/known/timestamppb storage_events.proto
