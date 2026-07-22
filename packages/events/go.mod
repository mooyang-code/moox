module github.com/mooyang-code/moox/packages/events

go 1.25.0

replace github.com/mooyang-code/moox/packages/jetstream => ../jetstream

require (
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
