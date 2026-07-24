PROTO_FILES := eventbus.proto
OUTPUT_DIR := ./eventbusgen
COMMON_PROTO_DIR := ../../../packages/commonpb

.PHONY: all clean

all:
	@mkdir -p $(OUTPUT_DIR)
	@for proto in $(PROTO_FILES); do \
		echo "trpc-open create $$proto"; \
		trpc-open create \
			-p "$$proto" \
			--protodir . \
			--protodir $(COMMON_PROTO_DIR) \
			--rpconly \
			--nogomod \
			--mock=false \
			-o $(OUTPUT_DIR) || exit 1; \
		sed -i.bak 's/source: eventbus_rpc\.proto/source: eventbus.proto/' $(OUTPUT_DIR)/eventbus.trpc.go; \
		rm -f $(OUTPUT_DIR)/eventbus.trpc.go.bak; \
		protoc --proto_path=. \
			--proto_path=$(COMMON_PROTO_DIR) \
			--go_out=paths=source_relative:$(OUTPUT_DIR) \
			--go_opt=Mgoogle/protobuf/timestamp.proto=google.golang.org/protobuf/types/known/timestamppb \
			"$$proto" || exit 1; \
	done

clean:
	rm -rf $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go $(OUTPUT_DIR)/.eventbus_rpc.*
