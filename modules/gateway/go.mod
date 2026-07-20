module github.com/mooyang-code/moox/modules/gateway

go 1.25.0

require (
	github.com/dgraph-io/badger/v4 v4.7.0
	github.com/mooyang-code/moox/modules/storage/proto/storagegen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/gatewayauth v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/gatewayproxy v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/timerjob v0.0.0-00010101000000-000000000000
	github.com/prometheus/client_golang v1.23.2
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1
	trpc.group/trpc-go/trpc-database/timer v1.0.0
	trpc.group/trpc-go/trpc-go v1.0.4
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/go-playground/form/v4 v4.2.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/mooyang-code/moox/packages/commonpb v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/messagepb v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/metricspb v0.0.0-00010101000000-000000000000 // indirect
	github.com/nats-io/nats.go v1.51.0 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
)

require (
	github.com/BurntSushi/toml v1.3.2 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/lestrrat-go/strftime v1.0.6 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mooyang-code/moox/packages/report v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/security v0.0.0-00010101000000-000000000000 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/panjf2000/ants/v2 v2.8.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/spf13/cast v1.5.1 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.48.0 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.37.0 // indirect
	go.opentelemetry.io/otel/metric v1.37.0 // indirect
	go.opentelemetry.io/otel/trace v1.37.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/automaxprocs v1.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.25.0 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	trpc.group/trpc-go/tnet v1.0.1 // indirect
	trpc.group/trpc/trpc-protocol/pb/go/trpc v1.0.1 // indirect
)

replace github.com/mooyang-code/moox/packages/gatewayauth => ../../packages/gatewayauth

replace github.com/mooyang-code/moox/packages/gatewayproxy => ../../packages/gatewayproxy

replace github.com/mooyang-code/moox/packages/security => ../../packages/security

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/timerjob => ../../packages/timerjob

replace github.com/mooyang-code/moox/packages/report => ../../packages/report

replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream

replace github.com/mooyang-code/moox/packages/messagepb => ../../packages/messagepb

replace github.com/mooyang-code/moox/packages/metricspb => ../../packages/metricspb

replace github.com/mooyang-code/moox/modules/storage/proto/storagegen => ../storage/proto/storagegen

replace github.com/mooyang-code/moox/packages/commonpb => ../../packages/commonpb
