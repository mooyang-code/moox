module github.com/mooyang-code/moox/web-host

go 1.24.0

require (
	github.com/mooyang-code/moox/packages/healthz v0.0.0-00010101000000-000000000000
	github.com/rakyll/statik v0.1.7
)

replace github.com/mooyang-code/moox/packages/healthz => ../packages/healthz
