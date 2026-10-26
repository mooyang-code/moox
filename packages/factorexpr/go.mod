module github.com/mooyang-code/moox/packages/factorexpr

go 1.27.2

require (
	github.com/expr-lang/expr v1.17.8
	github.com/mooyang-code/moox/packages/factorname v0.0.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/mooyang-code/moox/packages/factorname => ../factorname
