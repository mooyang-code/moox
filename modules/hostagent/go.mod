module github.com/mooyang-code/moox/modules/hostagent

go 1.24.0

require (
	github.com/google/uuid v1.6.0
	github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/hostmetricpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/messagepb v0.0.0-00010101000000-000000000000
	github.com/nats-io/nats.go v1.47.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	trpc.group/trpc-go/trpc-go v1.0.3
)

replace github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen => ./proto/hostagentgen
replace github.com/mooyang-code/moox/packages/hostmetricpb => ../../packages/hostmetricpb
replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream
replace github.com/mooyang-code/moox/packages/messagepb => ../../packages/messagepb
