module github.com/mooyang-code/moox/packages/seriesreplay

go 1.27.2

require (
	github.com/glebarez/go-sqlite v1.21.2
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
)

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/security => ../../packages/security
