PROTO_FILES := moox_message.proto
OUTPUT_DIR := .

.PHONY: all clean

all:
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
	rm -f *.pb.go *.trpc.go
