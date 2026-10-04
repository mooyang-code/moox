module github.com/mooyang-code/moox/packages/events

go 1.25.0

require (
	github.com/mooyang-code/moox/packages/cloudjobpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/hostmetricpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/metricspb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/marketfetchpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/observabilitypb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/storagepb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/tradeeventpb v0.0.0-00010101000000-000000000000
	github.com/nats-io/nats.go v1.51.0
	github.com/stretchr/testify v1.11.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/BurntSushi/toml v1.3.2 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/lestrrat-go/strftime v1.0.6 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/panjf2000/ants/v2 v2.8.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/spf13/cast v1.5.1 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.48.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/automaxprocs v1.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.25.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	trpc.group/trpc-go/tnet v1.0.1 // indirect
	trpc.group/trpc-go/trpc-go v1.0.4 // indirect
	trpc.group/trpc/trpc-protocol/pb/go/trpc v1.0.1 // indirect
)

replace github.com/mooyang-code/moox/packages/cloudjobpb => ../cloudjobpb

replace github.com/mooyang-code/moox/packages/hostmetricpb => ../hostmetricpb

replace github.com/mooyang-code/moox/packages/jetstream => ../jetstream

replace github.com/mooyang-code/moox/packages/metricspb => ../metricspb

replace github.com/mooyang-code/moox/packages/marketfetchpb => ../marketfetchpb

replace github.com/mooyang-code/moox/packages/observabilitypb => ../observabilitypb

replace github.com/mooyang-code/moox/packages/storagepb => ../storagepb

replace github.com/mooyang-code/moox/packages/tradeeventpb => ../tradeeventpb
