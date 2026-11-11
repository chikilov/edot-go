// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"errors"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
	"go.opentelemetry.io/otel/propagation"
)

// KafkaOption instruments franz-go producer and consumer calls. It propagates
// trace context and leaves application baggage and record keys out of telemetry.
func (s *SDK) KafkaOption() kgo.Opt {
	meter := kotel.NewMeter(kotel.MeterProvider(s.MeterProvider))
	return kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(s.kafkaTracer()), kotel.WithMeter(meter)).Hooks()...)
}

func (s *SDK) kafkaTracer() *kotel.Tracer {
	return kotel.NewTracer(
		kotel.TracerProvider(s.TracerProvider),
		kotel.TracerPropagator(propagation.TraceContext{}),
		kotel.KeyFormatter(func(*kgo.Record) (string, error) {
			return "", errKafkaKeyOmitted
		}),
	)
}

var errKafkaKeyOmitted = errors.New("Kafka record key omitted from telemetry")
