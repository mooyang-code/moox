# 生成代码与 directory.proto 放在同一目录（trpc-open 在临时目录生成后拷回，避免它在当前目录写入多余文件）。
TMP_DIR := ./tmp-gen

.PHONY: all clean

all:
	@rm -rf $(TMP_DIR) && mkdir -p $(TMP_DIR)
	trpc-open create -p ./directory.proto --protodir . --rpconly --nogomod --mock=false -o $(TMP_DIR)
	cp $(TMP_DIR)/directory.pb.go $(TMP_DIR)/directory.trpc.go .
	@rm -rf $(TMP_DIR)

clean:
	rm -f ./*.pb.go ./*.trpc.go
