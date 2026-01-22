package main

import (
	"context"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

var logger *slog.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// Prometheus histogram to carry exemplars
var reqDuration = prometheus.NewHistogramVec(
    prometheus.HistogramOpts{
        Name:    "http_request_duration_seconds",
        Help:    "HTTP request duration seconds",
        Buckets: prometheus.DefBuckets,
    },
    []string{"method", "status"},
)

func main() {
	// Initialize OpenTelemetry
	ctx := context.Background()
	shutdown := initOTel(ctx)
	defer shutdown(ctx)

	// Setup HTTP handlers with automatic tracing
	http.Handle("/healthz", otelhttp.NewHandler(http.HandlerFunc(healthzHandler), "healthz"))
	http.Handle("/work", otelhttp.NewHandler(http.HandlerFunc(workHandler), "work"))

	// Register Prometheus metrics
	prometheus.MustRegister(reqDuration)
	http.Handle("/metrics", promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))
	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func initOTel(ctx context.Context) func(context.Context) {
	// Create resource (identifies this service)
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("sample-app"),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	if err != nil {
		log.Fatalf("failed to create resource: %v", err)
	}

	// Get OTel Collector endpoint
	otelEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if otelEndpoint == "" {
		otelEndpoint = "otel-collector:4317"
	}

	// Setup trace exporter
	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(otelEndpoint),
	)
	if err != nil {
		log.Fatalf("failed to create trace exporter: %v", err)
	}

	// Setup trace provider
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	// Setup metric exporter
	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithInsecure(),
		otlpmetricgrpc.WithEndpoint(otelEndpoint),
	)
	if err != nil {
		log.Fatalf("failed to create metric exporter: %v", err)
	}

	// Setup metric provider
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	// Return cleanup function
	return func(ctx context.Context) {
		tracerProvider.Shutdown(ctx)
		meterProvider.Shutdown(ctx)
	}
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func workHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := http.StatusOK

	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	traceID := span.SpanContext().TraceID().String()
	log := logger.With(
		"trace_id", traceID,
		"span_id", span.SpanContext().SpanID().String(),
	)

	// Nested span to simulate work
	_, childSpan := otel.Tracer("app").Start(ctx, "simulate_work")
	latency := time.Duration(rand.Intn(400)) * time.Millisecond
	time.Sleep(latency)
	childSpan.End()

	_, cacheSpan := otel.Tracer("app").Start(ctx, "db_cache_lookup")
	time.Sleep(time.Duration(rand.Intn(200)) * time.Millisecond)
	cacheSpan.End()

	// the code fails 20% of the time
	if rand.Float32() < 0.2 {
		status = http.StatusInternalServerError
		log.Error("request failed",
			"latency_ms", latency.Milliseconds(),
			"status", status,
		)

		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	} else {
		log.Info("request succeeded",
		"latency_ms", latency.Milliseconds(),
		"status", status,
		)

		w.Write([]byte("Work completed\n"))
	}	

	// Record request duration with exemplar
	duration := time.Since(start).Seconds()
    obs := reqDuration.WithLabelValues(r.Method, strconv.Itoa(status))
    
    // If exemplar observer is supported, attach trace ID
    if exemplarObs, ok := obs.(prometheus.ExemplarObserver); ok && traceID != "" {
		log.Info("Attaching exemplar", "traceID", traceID, "duration", duration) 
        exemplarObs.ObserveWithExemplar(duration, prometheus.Labels{"traceID": traceID})
    } else {
		log.Warn("Exemplar not supported or traceID empty", "traceID", traceID, "ok", ok)
        obs.Observe(duration)
    }
}
