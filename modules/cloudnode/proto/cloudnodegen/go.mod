module github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen

go 1.24.0

require (
	github.com/mooyang-code/moox/packages/commonpb v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.11
	trpc.group/trpc-go/trpc-go v1.0.3
)

replace github.com/mooyang-code/moox/packages/commonpb => ../../../../packages/commonpb
