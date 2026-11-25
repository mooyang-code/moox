PROTOC ?= protoc

.PHONY: all clean

all:
	$(PROTOC) --proto_path=. --go_out=. --go_opt=paths=source_relative market_fetch_events.proto

clean:
	rm -f market_fetch_events.pb.go
