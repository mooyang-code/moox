package wirecompress

import "github.com/prometheus/client_golang/prometheus"

var (
	bytesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_wirecompress_bytes_total",
		Help: "Bytes processed by wirecompress; compare kind=raw with kind=compressed for the ratio.",
	}, []string{"use", "op", "kind", "dict"})
	errorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_wirecompress_errors_total",
		Help: "wirecompress failures by reason.",
	}, []string{"use", "op", "reason"})
)

func init() {
	prometheus.MustRegister(bytesTotal, errorsTotal)
}

func recordEncode(use Use, dict string, raw, compressed int) {
	bytesTotal.WithLabelValues(string(use), "encode", "raw", dict).Add(float64(raw))
	bytesTotal.WithLabelValues(string(use), "encode", "compressed", dict).Add(float64(compressed))
}

func recordDecode(use Use, dict string, compressed, raw int) {
	bytesTotal.WithLabelValues(string(use), "decode", "compressed", dict).Add(float64(compressed))
	bytesTotal.WithLabelValues(string(use), "decode", "raw", dict).Add(float64(raw))
}

func recordError(use Use, op, reason string) {
	errorsTotal.WithLabelValues(string(use), op, reason).Inc()
}
