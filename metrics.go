package tusdfiber

import (
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds Prometheus collectors for TUS operations.
type Metrics struct {
	UploadsCreated    prometheus.Counter
	UploadsFinished   prometheus.Counter
	UploadsTerminated prometheus.Counter
	BytesReceived     prometheus.Counter
	ErrorsTotal       *prometheus.CounterVec
	RequestsTotal     *prometheus.CounterVec
	ActiveUploads     prometheus.Gauge
}

// NewMetrics registers and returns TUS metrics with the given Prometheus registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	factory := promauto.With(reg)

	return &Metrics{
		UploadsCreated: factory.NewCounter(prometheus.CounterOpts{
			Name: "tusdfiber_uploads_created_total",
			Help: "Total number of uploads created.",
		}),
		UploadsFinished: factory.NewCounter(prometheus.CounterOpts{
			Name: "tusdfiber_uploads_finished_total",
			Help: "Total number of uploads finished.",
		}),
		UploadsTerminated: factory.NewCounter(prometheus.CounterOpts{
			Name: "tusdfiber_uploads_terminated_total",
			Help: "Total number of uploads terminated.",
		}),
		BytesReceived: factory.NewCounter(prometheus.CounterOpts{
			Name: "tusdfiber_bytes_received_total",
			Help: "Total number of bytes received across all uploads.",
		}),
		ErrorsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "tusdfiber_errors_total",
			Help: "Total number of TUS errors by error code.",
		}, []string{"code"}),
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "tusdfiber_requests_total",
			Help: "Total number of TUS requests by method.",
		}, []string{"method"}),
		ActiveUploads: factory.NewGauge(prometheus.GaugeOpts{
			Name: "tusdfiber_active_uploads",
			Help: "Current number of active (in-progress) uploads.",
		}),
	}
}

// Middleware returns a Fiber handler that counts requests and active uploads.
func (m *Metrics) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		m.RequestsTotal.WithLabelValues(c.Method()).Inc()
		if c.Method() == fiber.MethodPatch {
			m.ActiveUploads.Inc()
			defer m.ActiveUploads.Dec()
		}
		return c.Next()
	}
}

// PromHTTPHandler returns the standard Prometheus HTTP handler (http.Handler)
// so you can mount it via Fiber's adaptor:
//
//	import "github.com/gofiber/fiber/v2/middleware/adaptor"
//	app.Get("/metrics", adaptor.HTTPHandler(tusdfiber.PromHTTPHandler()))
func PromHTTPHandler() http.Handler {
	return promhttp.Handler()
}
