PROTO_FILES := common.proto metadata.proto rows.proto dataset_markers.proto data_node.proto primary_store.proto view.proto view_index.proto
OUTPUT_DIR := ./storagegen
COMMON_PROTO_DIR := ../../../packages/commonpb

.PHONY: all clean

all:
	@mkdir -p $(OUTPUT_DIR)
	@for proto in $(PROTO_FILES); do \
		echo "trpc-open create $$proto"; \
		trpc-open create \
			-p "./$$proto" \
			--protodir . \
			--protodir $(COMMON_PROTO_DIR) \
			--rpconly \
			--nogomod \
			--mock=false \
			-o $(OUTPUT_DIR) || exit 1; \
	done

clean:
	rm -rf $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go
