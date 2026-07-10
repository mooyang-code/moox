PROTO_FILES := eventbus.proto
OUTPUT_DIR := ./eventbusgen
COMMON_PROTO_DIR := ../../../packages/commonpb
MESSAGE_PROTO_DIR := ../../../packages/messagepb

.PHONY: all clean

# trpc-open cannot resolve an enum imported from another Go module. The recipe
# generates transport stubs from an int32-only view, then protoc regenerates
# eventbus.pb.go so TopicInfo.kind remains messagepb.MessageKind.

all:
	@mkdir -p $(OUTPUT_DIR)
	@for proto in $(PROTO_FILES); do \
		echo "trpc-open create $$proto"; \
		tmp_proto=eventbus_rpc.proto; \
		sed 's/trpc\.moox\.message\.MessageKind kind = 3;/int32 kind = 3;/' "$$proto" > "$$tmp_proto"; \
		trpc-open create \
			-p "$$tmp_proto" \
			--protodir . \
			--protodir $(COMMON_PROTO_DIR) \
			--protodir $(MESSAGE_PROTO_DIR) \
			--rpconly \
			--nogomod \
			--mock=false \
			-o $(OUTPUT_DIR) || { rm -f "$$tmp_proto"; exit 1; }; \
		mv -f $(OUTPUT_DIR)/eventbus_rpc.trpc.go $(OUTPUT_DIR)/eventbus.trpc.go; \
		sed -i.bak 's/source: eventbus_rpc\.proto/source: eventbus.proto/' $(OUTPUT_DIR)/eventbus.trpc.go; \
		rm -f $(OUTPUT_DIR)/eventbus_rpc.pb.go "$$tmp_proto"; \
		rm -f $(OUTPUT_DIR)/eventbus.trpc.go.bak; \
		protoc --proto_path=. \
			--proto_path=$(COMMON_PROTO_DIR) \
			--proto_path=$(MESSAGE_PROTO_DIR) \
			--go_out=paths=source_relative:$(OUTPUT_DIR) \
			--go_opt=Mgoogle/protobuf/timestamp.proto=google.golang.org/protobuf/types/known/timestamppb \
			"$$proto" || exit 1; \
	done

clean:
	rm -rf $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go $(OUTPUT_DIR)/.eventbus_rpc.*
