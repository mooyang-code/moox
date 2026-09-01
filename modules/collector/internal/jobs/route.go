package jobs

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/jobs/kline"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/symbol"
)

const (
	JobTypeCollectBinanceKline      = kline.JobType
	JobTypeCollectBinanceInstrument = symbol.JobType
)

// JobRoute maps one collector provider/data type to its queue identity.
type JobRoute struct {
	Exchange string
	DataType string
	JobType  string
}

var jobRoutes = []JobRoute{
	{Exchange: "binance", DataType: "kline", JobType: JobTypeCollectBinanceKline},
	{Exchange: "binance", DataType: "instrument", JobType: JobTypeCollectBinanceInstrument},
}

func init() {
	if err := validateJobRoutes(jobRoutes); err != nil {
		panic(err)
	}
}

// JobRouteFor returns the queue route for a provider/data type pair.
func JobRouteFor(exchange, dataType string) (JobRoute, bool) {
	exchange = normalizeRoutePart(exchange)
	dataType = normalizeRoutePart(dataType)
	for _, route := range jobRoutes {
		if route.Exchange == exchange && route.DataType == dataType {
			return route, true
		}
	}
	return JobRoute{}, false
}

// JobRouteByJobType returns the route for an exact queue job type.
func JobRouteByJobType(jobType string) (JobRoute, bool) {
	jobType = strings.TrimSpace(jobType)
	for _, route := range jobRoutes {
		if route.JobType == jobType {
			return route, true
		}
	}
	return JobRoute{}, false
}

// SupportedJobTypes returns the registered queue job types in stable order.
func SupportedJobTypes() []string {
	out := make([]string, 0, len(jobRoutes))
	for _, route := range jobRoutes {
		out = append(out, route.JobType)
	}
	return out
}

// ValidateJobTypes rejects queue identities that are not compiled into this worker.
func ValidateJobTypes(jobTypes []string) error {
	for _, jobType := range jobTypes {
		if _, ok := JobRouteByJobType(jobType); !ok {
			return fmt.Errorf("unsupported collector job type: %s", strings.TrimSpace(jobType))
		}
	}
	return nil
}

func validateJobRoutes(routes []JobRoute) error {
	identities := make(map[string]struct{}, len(routes))
	jobTypes := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		identity := normalizeRoutePart(route.Exchange) + "\x00" + normalizeRoutePart(route.DataType)
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("duplicate collector job route: exchange=%s data_type=%s", route.Exchange, route.DataType)
		}
		identities[identity] = struct{}{}

		jobType := strings.TrimSpace(route.JobType)
		if _, exists := jobTypes[jobType]; exists {
			return fmt.Errorf("duplicate collector job type: %s", jobType)
		}
		jobTypes[jobType] = struct{}{}
	}
	return nil
}

func normalizeRoutePart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
