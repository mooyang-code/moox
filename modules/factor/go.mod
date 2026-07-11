module github.com/mooyang-code/moox/modules/factor

go 1.24.0

require (
	github.com/glebarez/sqlite v1.11.0
	github.com/mooyang-code/go-commlib/trpc-database/timer v0.0.2
	github.com/mooyang-code/moox/modules/factor/proto/factorgen v0.0.0-20260709100258-8b1b64632c9f
	github.com/mooyang-code/moox/modules/storage/proto/gen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/commonpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/healthz v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/messagepb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/metricspb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/pyruntime v0.0.0-00010101000000-000000000000
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2
	github.com/prometheus/common v0.67.5
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/gorm v1.31.2
	trpc.group/trpc-go/trpc-filter/validation v1.0.1
	trpc.group/trpc-go/trpc-go v1.0.3
	trpc.group/trpc-go/trpc-log-cls v1.0.0
	trpc.group/trpc-go/trpc-metrics-prometheus v1.0.0
)

replace github.com/mooyang-code/moox/modules/storage/proto/gen => ../storage/proto/gen

replace github.com/mooyang-code/moox/packages/commonpb => ../../packages/commonpb

replace github.com/mooyang-code/moox/packages/healthz => ../../packages/healthz

replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream

replace github.com/mooyang-code/moox/packages/messagepb => ../../packages/messagepb

replace github.com/mooyang-code/moox/packages/metricspb => ../../packages/metricspb

replace github.com/mooyang-code/moox/packages/pyruntime => ../../packages/pyruntime

require (
	github.com/BurntSushi/toml v1.3.2 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/apache/arrow-go/v18 v18.4.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.4.9 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/go-playground/form/v4 v4.2.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/lestrrat-go/strftime v1.0.6 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nats.go v1.47.0 // indirect
	github.com/nats-io/nkeys v0.4.11 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/panjf2000/ants/v2 v2.4.6 // indirect
	github.com/pierrec/lz4 v2.6.1+incompatible // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/smartystreets/goconvey v1.7.2 // indirect
	github.com/spf13/cast v1.3.1 // indirect
	github.com/tencentcloud/tencentcloud-cls-sdk-go v0.0.0-20211222035622-e30dab6428ed // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.43.0 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	go.uber.org/automaxprocs v1.6.0 // indirect
	go.uber.org/multierr v1.8.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/mod v0.32.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/telemetry v0.0.0-20260109210033-bd525da824e2 // indirect
	golang.org/x/text v0.33.0 // indirect
	golang.org/x/tools v0.41.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	modernc.org/libc v1.67.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.45.0 // indirect
	trpc.group/trpc-go/tnet v1.0.1 // indirect
	trpc.group/trpc/trpc-protocol/pb/go/trpc v1.0.1 // indirect
)
