PROTO_FILES := dlq_payload.proto

.PHONY: all clean

all:
	@for proto in $(PROTO_FILES); do \
		protoc --go_out=. --go_opt=paths=source_relative $$proto || exit 1; \
	done

clean:
	rm -f *.pb.go
