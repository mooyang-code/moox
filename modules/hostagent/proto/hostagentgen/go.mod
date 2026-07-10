module github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen

go 1.24.0

require (
	github.com/mooyang-code/moox/packages/hostmetricpb v0.0.0-00010101000000-000000000000
	trpc.group/trpc-go/trpc-go v1.0.3
)

replace github.com/mooyang-code/moox/packages/hostmetricpb => ../../../../packages/hostmetricpb
