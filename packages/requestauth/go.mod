module github.com/mooyang-code/moox/packages/requestauth

go 1.26.0

require github.com/mooyang-code/moox/packages/security v0.0.0-00010101000000-000000000000

require (
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/mooyang-code/moox/packages/security => ../security
