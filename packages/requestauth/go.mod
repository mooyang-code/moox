module github.com/mooyang-code/moox/packages/requestauth

go 1.24.0

require github.com/mooyang-code/moox/packages/crypto v0.0.0-00010101000000-000000000000

require (
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	golang.org/x/crypto v0.46.0 // indirect
)

replace github.com/mooyang-code/moox/packages/crypto => ../crypto
