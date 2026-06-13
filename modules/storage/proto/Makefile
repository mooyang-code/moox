all:
    # 注意：原始命令是trpc 而不是trpc-open； 这里我是为了与本地内部版本的trpc工具区分，重命名了该工具名
	trpc-open create \
		-p ./common.proto \
		--rpconly \
		--nogomod \
		--mock=false \
		-o ./gen \

	trpc-open create \
		-p ./metadata.proto \
		--rpconly \
		--nogomod \
		--mock=false \
		-o ./gen \

	trpc-open create \
		-p ./access.proto \
		--rpconly \
		--nogomod \
		--mock=false \
		-o ./gen \

	trpc-open create \
		-p ./adapter.proto \
		--rpconly \
		--nogomod \
		--mock=false \
		-o ./gen \

	trpc-open create \
		-p ./message.proto \
		--rpconly \
		--nogomod \
		--mock=false \
		-o ./gen \

	trpc-open create \
		-p ./dbmanager.proto \
		--rpconly \
		--nogomod \
		--mock=false \
		-o ./gen \

.PHONY: all
