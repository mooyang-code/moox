PROTO_FILES := admin_service.proto ops_service.proto infra_service.proto secret_service.proto setup_service.proto sysdeploy_service.proto collector_publish_lease_service.proto gatewaycontrol.proto
OUTPUT_DIR := ./admingen
COMMON_PROTO_DIR := ../../../packages/commonpb
DIRECTORY_PROTO_DIR := ../../../packages/gatewayroute/proto

.PHONY: all clean

all:
	@mkdir -p $(OUTPUT_DIR)
	@for proto in $(PROTO_FILES); do \
		echo "trpc-open create $$proto"; \
		trpc-open create \
			-p "./$$proto" \
			--protodir . \
			--protodir $(COMMON_PROTO_DIR) \
			--protodir $(DIRECTORY_PROTO_DIR) \
			--rpconly \
			--nogomod \
			--mock=false \
			-o $(OUTPUT_DIR) || exit 1; \
	done

clean:
	rm -rf $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go
