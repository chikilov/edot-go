// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

// GRPCServerOption instruments incoming RPCs with this SDK's providers.
func (s *SDK) GRPCServerOption() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler(
		otelgrpc.WithTracerProvider(s.TracerProvider),
		otelgrpc.WithMeterProvider(s.MeterProvider),
		otelgrpc.WithPropagators(s.Propagator),
	))
}

// GRPCClientOption instruments outgoing RPCs and propagates the trace context.
func (s *SDK) GRPCClientOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler(
		otelgrpc.WithTracerProvider(s.TracerProvider),
		otelgrpc.WithMeterProvider(s.MeterProvider),
		otelgrpc.WithPropagators(s.Propagator),
	))
}
