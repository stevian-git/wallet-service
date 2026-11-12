package telemetry

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type contextKey struct{}

type pgxTracer struct {
	tracer trace.Tracer
}

func NewPGXTracer() pgx.QueryTracer {
	return &pgxTracer{tracer: otel.Tracer("wallet.pgx")}
}

func (t *pgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "pgx.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.statement", data.SQL),
		),
	)
	return context.WithValue(ctx, contextKey{}, span)
}

func (t *pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := spanFrom(ctx)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.SetAttributes(attribute.Int64("db.rows_affected", int64(data.CommandTag.RowsAffected())))
	span.End()
}

func (t *pgxTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "pgx.batch",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.Int("db.batch_size", data.Batch.Len())),
	)
	return context.WithValue(ctx, contextKey{}, span)
}

func (t *pgxTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	if data.Err != nil {
		if span, ok := spanFrom(ctx); ok {
			span.RecordError(data.Err)
		}
	}
}

func (t *pgxTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	span, ok := spanFrom(ctx)
	if !ok {
		return
	}
	if data.Err != nil && !errors.Is(data.Err, context.Canceled) && !errors.Is(data.Err, context.DeadlineExceeded) {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

func (t *pgxTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "pgx.copy_from",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.sql.table", data.TableName.Sanitize())),
	)
	return context.WithValue(ctx, contextKey{}, span)
}

func (t *pgxTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	span, ok := spanFrom(ctx)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.SetAttributes(attribute.Int64("db.rows_affected", int64(data.CommandTag.RowsAffected())))
	span.End()
}

func (t *pgxTracer) TracePrepareStart(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "pgx.prepare",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.statement", data.SQL),
			attribute.String("db.prepare.name", data.Name),
		),
	)
	return context.WithValue(ctx, contextKey{}, span)
}

func (t *pgxTracer) TracePrepareEnd(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareEndData) {
	span, ok := spanFrom(ctx)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

func spanFrom(ctx context.Context) (trace.Span, bool) {
	span, ok := ctx.Value(contextKey{}).(trace.Span)
	return span, ok
}
