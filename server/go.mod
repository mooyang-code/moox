module github.com/mooyang-code/moox/server

go 1.24.0

replace (
	github.com/mooyang-code/moox/server/internal/service => ./internal/service
	github.com/mooyang-code/moox/server/proto/gen => ./proto/gen
	github.com/mooyang-code/xData-mini/storage/proto => ../../xData-mini/storage/proto/gen
)

require (
	github.com/dgraph-io/badger/v4 v4.7.0
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/google/uuid v1.6.0
	github.com/mooyang-code/go-commlib/trpc-filter/cors v0.0.1
	github.com/mooyang-code/moox/server/proto/gen v0.0.0
	gopkg.in/yaml.v2 v2.4.0
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.30.0
	trpc.group/trpc-go/trpc-filter/validation v1.0.1
	trpc.group/trpc-go/trpc-go v1.0.3
)

require (
	github.com/BurntSushi/toml v0.3.1 // indirect
	github.com/andybalholm/brotli v1.0.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fsnotify/fsnotify v1.4.9 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/form/v4 v4.2.0 // indirect
	github.com/golang/snappy v0.0.3 // indirect
	github.com/google/flatbuffers v25.2.10+incompatible // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/lestrrat-go/strftime v1.0.6 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180228061459-e0a39a4cb421 // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/panjf2000/ants/v2 v2.4.6 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/spf13/cast v1.3.1 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.43.0 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.35.0 // indirect
	go.opentelemetry.io/otel/metric v1.35.0 // indirect
	go.opentelemetry.io/otel/trace v1.35.0 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	go.uber.org/automaxprocs v1.3.0 // indirect
	go.uber.org/multierr v1.6.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/sys v0.32.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	trpc.group/trpc-go/tnet v1.0.1 // indirect
	trpc.group/trpc/trpc-protocol/pb/go/trpc v1.0.1 // indirect
)
