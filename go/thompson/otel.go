package thompson

import (
	"context"
	"log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("thompson-sampling")

// OtelObserver emits real spans via otel.Tracer when a provider is configured,
// and always logs via log.Printf for local debugging (zero-dep fallback).
type OtelObserver struct {
	Service string
}

func NewOtelObserver(service string) *OtelObserver { return &OtelObserver{Service: service} }

func (o *OtelObserver) OnSelect(chosen string, scores map[string]float64) {
	ctx := context.Background()
	_, span := tracer.Start(ctx, "thompson.select")
	span.SetAttributes(
		attribute.String("service", o.Service),
		attribute.String("chosen", chosen),
	)
	span.End()
	log.Printf("[otel:%s] thompson.select chosen=%s scores=%v", o.Service, chosen, scores)
}
func (o *OtelObserver) OnRecord(arm string, reward float64, p Posterior) {
	ctx := context.Background()
	_, span := tracer.Start(ctx, "thompson.record")
	span.SetAttributes(
		attribute.String("service", o.Service),
		attribute.String("arm", arm),
		attribute.Float64("reward", reward),
		attribute.Float64("mean", p.Mean()),
		attribute.Int64("pulls", int64(p.Pulls)),
	)
	span.End()
	log.Printf("[otel:%s] thompson.record arm=%s reward=%.3f mean=%.3f pulls=%d", o.Service, arm, reward, p.Mean(), p.Pulls)
}
func (o *OtelObserver) OnArmAdded(id string, warmStarted bool) {
	ctx := context.Background()
	_, span := tracer.Start(ctx, "thompson.arm_added")
	span.SetAttributes(
		attribute.String("service", o.Service),
		attribute.String("id", id),
		attribute.Bool("warm_started", warmStarted),
	)
	span.End()
	log.Printf("[otel:%s] thompson.arm_added id=%s warmStarted=%v", o.Service, id, warmStarted)
}
func (o *OtelObserver) OnDiscount(factor float64) {
	ctx := context.Background()
	_, span := tracer.Start(ctx, "thompson.discount")
	span.SetAttributes(
		attribute.String("service", o.Service),
		attribute.Float64("factor", factor),
	)
	span.End()
	// Intentionally log-only: an otel Meter instrument must be created once and
	// cached on the observer (meter.Int64Counter returns an instrument + error).
	// Constructing a counter per OnDiscount call would allocate and can fail;
	// wire a cached counter at construction if metrics are needed.
	log.Printf("[otel:%s] thompson.discount factor=%g", o.Service, factor)
}
