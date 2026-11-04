module github.com/mooyang-code/moox/packages/seriesimport

go 1.27.3

require (
	github.com/glebarez/go-sqlite v1.21.2
	github.com/mooyang-code/moox/packages/barclock v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/seriesformat v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/mooyang-code/moox/packages/frequency v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/marketcalendar v0.0.0-00010101000000-000000000000 // indirect
)

replace github.com/mooyang-code/moox/packages/seriesformat => ../../packages/seriesformat

replace github.com/mooyang-code/moox/packages/barclock => ../../packages/barclock

replace github.com/mooyang-code/moox/packages/marketcalendar => ../../packages/marketcalendar

replace github.com/mooyang-code/moox/packages/frequency => ../../packages/frequency

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/security => ../../packages/security
