module github.com/mooyang-code/moox/modules/cli

go 1.24.0

replace github.com/mooyang-code/moox/modules/storage/proto/storagegen => ../storage/proto/storagegen

replace github.com/mooyang-code/moox/modules/admin/proto/admingen => ../admin/proto/admingen

require (
	github.com/mooyang-code/moox/modules/storage/proto/storagegen v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.9.1
	github.com/stretchr/testify v1.11.1
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls v1.3.131
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common v1.3.131
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	golang.org/x/crypto v0.47.0 // indirect
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
	github.com/mooyang-code/moox/packages/commonpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/crypto v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/gatewayauth v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000 // indirect
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
	go.uber.org/zap v1.25.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	trpc.group/trpc-go/tnet v1.0.1 // indirect
	trpc.group/trpc-go/trpc-go v1.0.4 // indirect
	trpc.group/trpc/trpc-protocol/pb/go/trpc v1.0.1 // indirect
)

replace github.com/mooyang-code/moox/packages/gatewayauth => ../../packages/gatewayauth

replace github.com/mooyang-code/moox/packages/crypto => ../../packages/crypto

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/commonpb => ../../packages/commonpb
