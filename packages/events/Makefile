PROTO_DIR := proto
MODULE := github.com/mooyang-code/moox/packages/events
PROTOBUF_PROTO_DIR := ../../modules/eventbus/proto
PROTO_FILES := eventpb/event_message.proto marketpb/market_payloads.proto tradingpb/trading_payloads.proto

.PHONY: all clean

all:
	@for proto in $(PROTO_FILES); do \
		echo "protoc $(PROTO_DIR)/$$proto"; \
		protoc --proto_path=$(PROTO_DIR) --proto_path=$(PROTOBUF_PROTO_DIR) --go_out=. --go_opt=module=$(MODULE) "$$proto" || exit 1; \
	done

clean:
	rm -f eventpb/*.pb.go marketpb/*.pb.go tradingpb/*.pb.go
