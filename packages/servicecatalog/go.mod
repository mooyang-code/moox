module github.com/mooyang-code/moox/packages/servicecatalog

go 1.25.0

require (
	github.com/mooyang-code/moox/packages/gatewayroute v0.0.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/mooyang-code/moox/packages/gatewayroute => ../gatewayroute
