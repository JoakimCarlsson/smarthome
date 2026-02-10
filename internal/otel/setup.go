package otel

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type Config struct {
	ServiceName    string
	ServiceVersion string
	OTLPEndpoint   string
	OTLPToken      string
}

func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	shutdown = func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		handleErr(err)
		return
	}

	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	useOTLP := cfg.OTLPEndpoint != "" && cfg.OTLPToken != ""

	tracerProvider, err := newTracerProvider(ctx, res, cfg, useOTLP)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	meterProvider, err := newMeterProvider(ctx, res, cfg, useOTLP)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	loggerProvider, err := newLoggerProvider(ctx, res, cfg, useOTLP)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return shutdown, nil
}

func newTracerProvider(
	ctx context.Context,
	res *resource.Resource,
	cfg Config,
	useOTLP bool,
) (*trace.TracerProvider, error) {
	if useOTLP {
		exporter, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
			otlptracehttp.WithHeaders(map[string]string{
				"Authorization": "Bearer " + cfg.OTLPToken,
			}),
		)
		if err != nil {
			return nil, err
		}
		return trace.NewTracerProvider(
			trace.WithBatcher(exporter),
			trace.WithResource(res),
		), nil
	}

	return trace.NewTracerProvider(
		trace.WithResource(res),
	), nil
}

func newMeterProvider(
	ctx context.Context,
	res *resource.Resource,
	cfg Config,
	useOTLP bool,
) (*metric.MeterProvider, error) {
	if useOTLP {
		exporter, err := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint),
			otlpmetrichttp.WithHeaders(map[string]string{
				"Authorization": "Bearer " + cfg.OTLPToken,
			}),
		)
		if err != nil {
			return nil, err
		}
		return metric.NewMeterProvider(
			metric.WithReader(metric.NewPeriodicReader(exporter)),
			metric.WithResource(res),
		), nil
	}

	return metric.NewMeterProvider(
		metric.WithResource(res),
	), nil
}

func newLoggerProvider(
	ctx context.Context,
	res *resource.Resource,
	cfg Config,
	useOTLP bool,
) (*log.LoggerProvider, error) {
	if useOTLP {
		exporter, err := otlploghttp.New(ctx,
			otlploghttp.WithEndpoint(cfg.OTLPEndpoint),
			otlploghttp.WithHeaders(map[string]string{
				"Authorization": "Bearer " + cfg.OTLPToken,
			}),
		)
		if err != nil {
			return nil, err
		}
		return log.NewLoggerProvider(
			log.WithProcessor(log.NewBatchProcessor(exporter)),
			log.WithResource(res),
		), nil
	}

	return log.NewLoggerProvider(
		log.WithResource(res),
	), nil
}
