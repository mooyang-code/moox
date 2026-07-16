PROTO_FILES := admin_service.proto ops_service.proto infra_service.proto secret_service.proto setup_service.proto sysdeploy_service.proto
OUTPUT_DIR := ./admingen
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
