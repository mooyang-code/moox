PROTO_DIR := proto
MODULE := github.com/mooyang-code/moox/packages/events
PROTO_FILES := eventpb/event_message.proto eventpb/storage_events.proto eventpb/trade_events.proto

.PHONY: all clean

all:
	@for proto in $(PROTO_FILES); do \
		echo "protoc $(PROTO_DIR)/$$proto"; \
		protoc --proto_path=$(PROTO_DIR) --go_out=. --go_opt=module=$(MODULE) "$$proto" || exit 1; \
	done

clean:
	rm -f eventpb/*.pb.go
