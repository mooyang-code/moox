PROTOC ?= protoc

.PHONY: generate
generate:
	$(PROTOC) --proto_path=. --go_out=. --go_opt=paths=source_relative storage_events.proto
