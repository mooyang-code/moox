module github.com/mooyang-code/moox/packages/gatewayauth

go 1.24.0

require (
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/security v0.0.0-00010101000000-000000000000
)

require (
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	golang.org/x/crypto v0.47.0 // indirect
)

replace github.com/mooyang-code/moox/packages/security => ../security

replace github.com/mooyang-code/moox/packages/requestauth => ../requestauth
