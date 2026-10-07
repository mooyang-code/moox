package jobs

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/jobs/kline"
)

const (
	JobTypeCollectBinanceKline = kline.JobType
)

// JobRoute maps one collector provider/data type to its queue identity.
type JobRoute struct {
	Exchange string
	DataType string
	JobType  string
}

var jobRoutes = []JobRoute{
	{Exchange: "binance", DataType: "kline", JobType: JobTypeCollectBinanceKline},
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
