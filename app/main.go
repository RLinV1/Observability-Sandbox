package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

func main() {
	// Initialize OpenTelemetry
	ctx := context.Background()
	shutdown := initOTel(ctx)
	defer shutdown(ctx)

	// Setup HTTP handlers with automatic tracing
	http.Handle("/healthz", otelhttp.NewHandler(http.HandlerFunc(healthzHandler), "healthz"))
	http.Handle("/work", otelhttp.NewHandler(http.HandlerFunc(workHandler), "work"))

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
	// Sleep for random time (0-500ms)
	latency := time.Duration(rand.Intn(500)) * time.Millisecond
	time.Sleep(latency)

	// 20% chance of error
	if rand.Float32() < 0.2 {
		fmt.Printf("ERROR: Request failed after %v\n", latency)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	fmt.Printf("SUCCESS: Request completed in %v\n", latency)
	w.Write([]byte("Work completed\n"))
}