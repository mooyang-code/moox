module github.com/mooyang-code/moox/modules/cli

go 1.26.9

replace github.com/mooyang-code/moox/modules/storage/proto/storagegen => ../storage/proto/storagegen

replace github.com/mooyang-code/moox/modules/admin/proto/admingen => ../admin/proto/admingen

replace github.com/mooyang-code/moox/modules/collector/proto/collectorgen => ../collector/proto/collectorgen

require (
	github.com/glebarez/sqlite v1.11.0
	github.com/mooyang-code/moox/modules/admin/proto/admingen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/modules/collector/proto/collectorgen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/modules/storage/proto/storagegen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/cloudprovider v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/frequency v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/marketcalendar v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/servicecatalog v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/storagepolicy v0.0.0-00010101000000-000000000000
	github.com/pkg/sftp v1.13.10
	github.com/prometheus/client_golang v1.23.2
	github.com/spf13/cobra v1.9.1
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.50.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/gorm v1.31.2
	trpc.group/trpc-go/trpc-go v1.0.4
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/mooyang-code/moox/packages/events v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/hostmetricpb v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/marketfetchpb v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/metricspb v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/storagepb v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/tradeeventpb v0.0.0-00010101000000-000000000000 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nats.go v1.51.0 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls v1.3.135 // indirect
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common v1.3.135 // indirect
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/sts v1.1.11 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)

require (
	github.com/BurntSushi/toml v1.3.2
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/go-playground/form/v4 v4.2.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/lestrrat-go/strftime v1.0.6 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/modules/monitor/proto/monitorgen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/commonpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/doctor v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/gatewayauth v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/gatewayclient v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/report v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/security v0.0.0-00010101000000-000000000000
	github.com/panjf2000/ants/v2 v2.8.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/spf13/cast v1.5.1 // indirect
	github.com/spf13/pflag v1.0.6 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.48.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/automaxprocs v1.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.25.0
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	trpc.group/trpc-go/tnet v1.0.1 // indirect
	trpc.group/trpc/trpc-protocol/pb/go/trpc v1.0.1 // indirect
)

replace github.com/mooyang-code/moox/packages/gatewayauth => ../../packages/gatewayauth

replace github.com/mooyang-code/moox/packages/security => ../../packages/security

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/commonpb => ../../packages/commonpb

replace github.com/mooyang-code/moox/packages/cloudprovider => ../../packages/cloudprovider

replace github.com/mooyang-code/moox/modules/monitor/proto/monitorgen => ../monitor/proto/monitorgen

replace github.com/mooyang-code/moox/packages/doctor => ../../packages/doctor

replace github.com/mooyang-code/moox/packages/report => ../../packages/report

replace github.com/mooyang-code/moox/packages/frequency => ../../packages/frequency

replace github.com/mooyang-code/moox/packages/storagepolicy => ../../packages/storagepolicy

replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream

replace github.com/mooyang-code/moox/packages/metricspb => ../../packages/metricspb

replace github.com/mooyang-code/moox/packages/hostmetricpb => ../../packages/hostmetricpb

replace github.com/mooyang-code/moox/packages/gatewayclient => ../../packages/gatewayclient

replace github.com/mooyang-code/moox/packages/servicecatalog => ../../packages/servicecatalog

replace github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen => ../../packages/gatewayroute/proto/gatewayroutegen

replace github.com/mooyang-code/moox/packages/events => ../../packages/events

replace github.com/mooyang-code/moox/packages/marketcalendar => ../../packages/marketcalendar

replace github.com/mooyang-code/moox/packages/marketfetchpb => ../../packages/marketfetchpb

replace github.com/mooyang-code/moox/packages/tradeeventpb => ../../packages/tradeeventpb

replace github.com/mooyang-code/moox/packages/storagepb => ../../packages/storagepb

replace github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen => ../cloudnode/proto/cloudnodegen
