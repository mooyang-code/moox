PROTO_FILES := hostagent.proto
OUTPUT_DIR := ./hostagentgen

.PHONY: all clean
all:
	@mkdir -p $(OUTPUT_DIR)
	@for proto in $(PROTO_FILES); do trpc-open create -p "./$$proto" --protodir . --protodir ../../../packages/hostmetricpb --rpconly --nogomod --mock=false -o $(OUTPUT_DIR) || exit 1; done
clean:
	rm -rf $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go
