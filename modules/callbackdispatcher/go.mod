module github.com/mooyang-code/moox/modules/callbackdispatcher

go 1.27.3

require (
	github.com/glebarez/sqlite v1.11.0
	github.com/stretchr/testify v1.12.1
	gorm.io/gorm v1.31.2
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/libc v1.67.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.45.0 // indirect
)

replace github.com/mooyang-code/moox/packages/commonpb => ../../packages/commonpb

replace github.com/mooyang-code/moox/packages/healthz => ../../packages/healthz

replace github.com/mooyang-code/moox/packages/gatewayauth => ../../packages/gatewayauth

replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream

replace github.com/mooyang-code/moox/packages/events => ../../packages/events

replace github.com/mooyang-code/moox/packages/metricspb => ../../packages/metricspb

replace github.com/mooyang-code/moox/packages/report => ../../packages/report

replace github.com/mooyang-code/moox/packages/frequency => ../../packages/frequency

replace github.com/mooyang-code/moox/packages/gatewayclient => ../../packages/gatewayclient

replace github.com/mooyang-code/moox/packages/servicecatalog => ../../packages/servicecatalog

replace github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen => ../../packages/gatewayroute/proto/gatewayroutegen

replace github.com/mooyang-code/moox/packages/hostmetricpb => ../../packages/hostmetricpb

replace github.com/mooyang-code/moox/packages/marketfetchpb => ../../packages/marketfetchpb

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/security => ../../packages/security

replace github.com/mooyang-code/moox/packages/storagepb => ../../packages/storagepb

replace github.com/mooyang-code/moox/packages/tradeeventpb => ../../packages/tradeeventpb

replace github.com/mooyang-code/moox/packages/wirecompress => ../../packages/wirecompress

replace github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen => ./proto/callbackdispatchergen

replace github.com/mooyang-code/moox/packages/netguard => ../../packages/netguard
