PROTOC ?= protoc

.PHONY: all clean

all:
	$(PROTOC) --go_out=. --go_opt=paths=source_relative trade_events.proto

clean:
	rm -f trade_events.pb.go
