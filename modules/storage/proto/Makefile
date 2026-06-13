PROTO_FILES := common.proto metadata.proto data.proto query.proto adapter.proto message.proto
OUTPUT_DIR := ./genv2

.PHONY: all clean legacy-list

all:
	@mkdir -p $(OUTPUT_DIR)
	@for proto in $(PROTO_FILES); do \
		echo "trpc-open create $$proto"; \
		trpc-open create \
			-p "./$$proto" \
			--rpconly \
			--nogomod \
			--mock=false \
			-o $(OUTPUT_DIR) || exit 1; \
	done

clean:
	rm -rf $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go

legacy-list:
	@find legacy -maxdepth 1 -name '*.proto' -print | sort
