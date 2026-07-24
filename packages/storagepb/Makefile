PROTOC ?= protoc

.PHONY: generate
generate:
	$(PROTOC) --go_out=. --go_opt=paths=source_relative storage_events.proto
