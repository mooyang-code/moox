PROTOC ?= protoc
PROTOBUF_PROTO_DIR := ../../modules/eventbus/proto

.PHONY: all clean

all:
	$(PROTOC) --proto_path=. --proto_path=$(PROTOBUF_PROTO_DIR) --go_out=. --go_opt=paths=source_relative strategy_events.proto

clean:
	rm -f strategy_events.pb.go
