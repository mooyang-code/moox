module github.com/mooyang-code/moox/modules/trade

go 1.24.0

toolchain go1.24.1

require github.com/matoous/go-nanoid/v2 v2.1.0

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/stretchr/testify v1.11.1 // indirect
)

replace github.com/mooyang-code/moox/modules/trade/proto/tradegen => ./proto/tradegen
