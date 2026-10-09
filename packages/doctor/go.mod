module github.com/mooyang-code/moox/packages/doctor

go 1.26.9

require (
	github.com/mooyang-code/moox/packages/servicecatalog v0.0.0-00010101000000-000000000000
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
)

replace github.com/mooyang-code/moox/packages/servicecatalog => ../servicecatalog

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
