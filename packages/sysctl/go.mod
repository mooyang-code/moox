module github.com/mooyang-code/moox/packages/sysctl

go 1.27.2

require (
	github.com/mooyang-code/moox/packages/lifecycle v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.11.1
)

replace github.com/mooyang-code/moox/packages/lifecycle => ../../packages/lifecycle

replace github.com/mooyang-code/moox/packages/requestauth => ../../packages/requestauth

replace github.com/mooyang-code/moox/packages/security => ../../packages/security

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000 // indirect
	github.com/mooyang-code/moox/packages/security v0.0.0-00010101000000-000000000000 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
