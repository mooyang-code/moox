OUTPUT_DIR := ./gatewayroutegen

.PHONY: all clean

all:
	@mkdir -p $(OUTPUT_DIR)
	trpc-open create -p ./directory.proto --protodir . --rpconly --nogomod --mock=false -o $(OUTPUT_DIR)

clean:
	rm -f $(OUTPUT_DIR)/*.pb.go $(OUTPUT_DIR)/*.trpc.go
