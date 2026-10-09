PROTO_FILES := egress.proto
OUTPUT_DIR := ./egressgen
COMMON_PROTO_DIR := ../../../packages/commonpb

.PHONY: all clean

all:
	@mkdir -p $(OUTPUT_DIR)
	trpc-open create -p ./egress.proto --protodir . --protodir $(COMMON_PROTO_DIR) --rpconly --nogomod --mock=false -o $(OUTPUT_DIR)

clean:
	rm -f $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go
