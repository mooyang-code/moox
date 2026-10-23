module github.com/mooyang-code/moox/packages/barclock

go 1.25.0

require (
	github.com/mooyang-code/moox/packages/frequency v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/marketcalendar v0.0.0-00010101000000-000000000000
)

replace github.com/mooyang-code/moox/packages/frequency => ../frequency

replace github.com/mooyang-code/moox/packages/marketcalendar => ../marketcalendar
