module github.com/mooyang-code/moox/packages/jetstream

go 1.24.0

replace github.com/mooyang-code/moox/packages/messagepb => ../messagepb

require (
	github.com/mooyang-code/moox/packages/messagepb v0.0.0-00010101000000-000000000000
	github.com/nats-io/nats-server/v2 v2.11.3 // test-only embedded broker
	github.com/nats-io/nats.go v1.47.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/go-tpm v0.9.6 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/minio/highwayhash v1.0.3 // indirect
	github.com/nats-io/jwt/v2 v2.7.4 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)
