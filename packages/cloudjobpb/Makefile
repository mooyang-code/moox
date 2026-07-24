PROTOC ?= protoc
PROTOBUF_PROTO_DIR := ../../modules/eventbus/proto

.PHONY: all clean

all:
	$(PROTOC) --proto_path=. --proto_path=$(PROTOBUF_PROTO_DIR) --go_out=. --go_opt=paths=source_relative job_events.proto

clean:
	rm -f job_events.pb.go
