PROTO_FILES := health_check.proto

.PHONY: all clean

all:
	@for proto in $(PROTO_FILES); do \
		echo "protoc $$proto"; \
		protoc --proto_path=. \
			--proto_path=../../modules/eventbus/proto \
			--go_out=. \
			--go_opt=paths=source_relative \
			--go_opt=Mgoogle/protobuf/timestamp.proto=google.golang.org/protobuf/types/known/timestamppb \
			$$proto || exit 1; \
	done

clean:
	rm -f *.pb.go
