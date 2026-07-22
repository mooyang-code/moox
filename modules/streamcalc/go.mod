module github.com/mooyang-code/moox/modules/streamcalc

go 1.25.0

replace github.com/mooyang-code/moox/packages/events => ../../packages/events
replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream
replace github.com/mooyang-code/moox/modules/storage/proto/storagegen => ../storage/proto/storagegen

require (
	github.com/mooyang-code/moox/packages/events v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/modules/storage/proto/storagegen v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	trpc.group/trpc-go/trpc-go v1.0.4
	github.com/nats-io/nats-server/v2 v2.11.17
	github.com/nats-io/nats.go v1.51.0
)
