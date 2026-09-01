package marketdata

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
)

type outOfCoverageCalendar struct{}

func (outOfCoverageCalendar) ID() string { return "test" }
func (outOfCoverageCalendar) FirstDate() CivilDate {
	return marketcalendar.MustParseCivilDate("2020-01-01")
}
func (outOfCoverageCalendar) LastDate() CivilDate {
	return marketcalendar.MustParseCivilDate("2020-12-31")
}
func (outOfCoverageCalendar) Status(CivilDate) (CoverageStatus, error) { return OutOfCoverage, nil }

func TestTradabilityPolicyDistinguishesSessionAndCalendarCoverage(t *testing.T) {
	calendar, err := marketcalendar.Load("cn_stock")
	require.NoError(t, err)
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	policy := TradabilityPolicy{
		Calendar: calendar,
		Session:  SessionSpec{Location: location, Segments: []SessionSegment{{Open: 9*time.Hour + 30*time.Minute, Close: 11*time.Hour + 30*time.Minute}, {Open: 13 * time.Hour, Close: 15 * time.Hour}}},
	}

	status, err := policy.Status(time.Date(2026, 8, 31, 9, 30, 0, 0, location))
	require.NoError(t, err)
	require.Equal(t, Tradable, status)

	status, err = policy.Status(time.Date(2026, 8, 31, 11, 30, 0, 0, location))
	require.NoError(t, err)
	require.Equal(t, OutsideSession, status)

	status, err = policy.Status(time.Date(2026, 8, 30, 10, 0, 0, 0, location))
	require.NoError(t, err)
	require.Equal(t, NonTradingDayStatus, status)

	status, err = policy.Status(time.Date(2027, 1, 1, 10, 0, 0, 0, location))
	require.Error(t, err)
	require.ErrorIs(t, err, marketcalendar.ErrOutOfCoverage)
	require.Equal(t, OutOfCalendarCoverage, status)
}

func TestTradabilityPolicyRequiresCalendar(t *testing.T) {
	policy := TradabilityPolicy{Session: SessionSpec{Location: time.UTC, Segments: []SessionSegment{{Open: 0, Close: time.Hour}}}}
	_, err := policy.Status(time.Now())
	require.Error(t, err)
	require.False(t, errors.Is(err, marketcalendar.ErrOutOfCoverage))
}

func TestTradabilityPolicyRejectsOutOfCoverageBuckets(t *testing.T) {
	policy := TradabilityPolicy{
		Calendar: outOfCoverageCalendar{},
		Session:  SessionSpec{Location: time.FixedZone("Asia/Shanghai", 8*60*60), Segments: []SessionSegment{{Open: 9 * time.Hour, Close: 10 * time.Hour}}},
	}

	_, err := policy.ExpectedMinuteBuckets(marketcalendar.MustParseCivilDate("2027-01-01"))
	require.Error(t, err)
	require.ErrorIs(t, err, marketcalendar.ErrOutOfCoverage)
}
