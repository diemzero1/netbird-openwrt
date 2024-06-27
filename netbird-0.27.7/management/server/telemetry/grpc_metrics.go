package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/instrument"
	"go.opentelemetry.io/otel/metric/instrument/asyncint64"
	"go.opentelemetry.io/otel/metric/instrument/syncint64"
)

// GRPCMetrics are gRPC server metrics
type GRPCMetrics struct {
	meter                 metric.Meter
	syncRequestsCounter   syncint64.Counter
	loginRequestsCounter  syncint64.Counter
	getKeyRequestsCounter syncint64.Counter
	activeStreamsGauge    asyncint64.Gauge
	syncRequestDuration   syncint64.Histogram
	loginRequestDuration  syncint64.Histogram
	channelQueueLength    syncint64.Histogram
	ctx                   context.Context
}

// NewGRPCMetrics creates new GRPCMetrics struct and registers common metrics of the gRPC server
func NewGRPCMetrics(ctx context.Context, meter metric.Meter) (*GRPCMetrics, error) {
	syncRequestsCounter, err := meter.SyncInt64().Counter("management.grpc.sync.request.counter", instrument.WithUnit("1"))
	if err != nil {
		return nil, err
	}
	loginRequestsCounter, err := meter.SyncInt64().Counter("management.grpc.login.request.counter", instrument.WithUnit("1"))
	if err != nil {
		return nil, err
	}
	getKeyRequestsCounter, err := meter.SyncInt64().Counter("management.grpc.key.request.counter", instrument.WithUnit("1"))
	if err != nil {
		return nil, err
	}

	activeStreamsGauge, err := meter.AsyncInt64().Gauge("management.grpc.connected.streams", instrument.WithUnit("1"))
	if err != nil {
		return nil, err
	}

	syncRequestDuration, err := meter.SyncInt64().Histogram("management.grpc.sync.request.duration.ms", instrument.WithUnit("milliseconds"))
	if err != nil {
		return nil, err
	}

	loginRequestDuration, err := meter.SyncInt64().Histogram("management.grpc.login.request.duration.ms", instrument.WithUnit("milliseconds"))
	if err != nil {
		return nil, err
	}

	// We use histogram here as we have multiple channel at the same time and we want to see a slice at any given time
	// Then we should be able to extract min, manx, mean and the percentiles.
	// TODO(yury): This needs custom bucketing as we are interested in the values from 0 to server.channelBufferSize (100)
	channelQueue, err := meter.SyncInt64().Histogram(
		"management.grpc.updatechannel.queue",
		instrument.WithDescription("Number of update messages in the channel queue"),
		instrument.WithUnit("length"),
	)
	if err != nil {
		return nil, err
	}

	return &GRPCMetrics{
		meter:                 meter,
		syncRequestsCounter:   syncRequestsCounter,
		loginRequestsCounter:  loginRequestsCounter,
		getKeyRequestsCounter: getKeyRequestsCounter,
		activeStreamsGauge:    activeStreamsGauge,
		syncRequestDuration:   syncRequestDuration,
		loginRequestDuration:  loginRequestDuration,
		channelQueueLength:    channelQueue,
		ctx:                   ctx,
	}, err
}

// CountSyncRequest counts the number of gRPC sync requests coming to the gRPC API
func (grpcMetrics *GRPCMetrics) CountSyncRequest() {
	grpcMetrics.syncRequestsCounter.Add(grpcMetrics.ctx, 1)
}

// CountGetKeyRequest counts the number of gRPC get server key requests coming to the gRPC API
func (grpcMetrics *GRPCMetrics) CountGetKeyRequest() {
	grpcMetrics.getKeyRequestsCounter.Add(grpcMetrics.ctx, 1)
}

// CountLoginRequest counts the number of gRPC login requests coming to the gRPC API
func (grpcMetrics *GRPCMetrics) CountLoginRequest() {
	grpcMetrics.loginRequestsCounter.Add(grpcMetrics.ctx, 1)
}

// CountLoginRequestDuration counts the duration of the login gRPC requests
func (grpcMetrics *GRPCMetrics) CountLoginRequestDuration(duration time.Duration) {
	grpcMetrics.loginRequestDuration.Record(grpcMetrics.ctx, duration.Milliseconds())
}

// CountSyncRequestDuration counts the duration of the sync gRPC requests
func (grpcMetrics *GRPCMetrics) CountSyncRequestDuration(duration time.Duration) {
	grpcMetrics.syncRequestDuration.Record(grpcMetrics.ctx, duration.Milliseconds())
}

// RegisterConnectedStreams registers a function that collects number of active streams and feeds it to the metrics gauge.
func (grpcMetrics *GRPCMetrics) RegisterConnectedStreams(producer func() int64) error {
	return grpcMetrics.meter.RegisterCallback(
		[]instrument.Asynchronous{
			grpcMetrics.activeStreamsGauge,
		},
		func(ctx context.Context) {
			grpcMetrics.activeStreamsGauge.Observe(ctx, producer())
		},
	)
}

// UpdateChannelQueueLength update the histogram that keep distribution of the update messages channel queue
func (metrics *GRPCMetrics) UpdateChannelQueueLength(length int) {
	metrics.channelQueueLength.Record(metrics.ctx, int64(length))
}
