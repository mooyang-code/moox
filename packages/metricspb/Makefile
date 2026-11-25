PROTO_FILES := metrics.proto
OUTPUT_DIR := .
COMMON_PROTO_DIR := ../commonpb

.PHONY: all clean

all:
	@for proto in $(PROTO_FILES); do \
		echo "protoc $$proto"; \
		protoc --proto_path=. --proto_path=$(COMMON_PROTO_DIR) --go_out=$(OUTPUT_DIR) --go_opt=paths=source_relative $$proto || exit 1; \
	done

clean:
	rm -f *.pb.go
