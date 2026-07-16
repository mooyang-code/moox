module github.com/mooyang-code/moox/packages/gatewayauth

go 1.24.0

require (
	github.com/mooyang-code/moox/packages/crypto v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/requestauth v0.0.0-00010101000000-000000000000
)

replace github.com/mooyang-code/moox/packages/crypto => ../crypto

replace github.com/mooyang-code/moox/packages/requestauth => ../requestauth
