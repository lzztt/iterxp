package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	traceServiceName   = "iterxp-agent-v2"
	traceAgentName     = "iterxp-agent-v2"
	traceSchemaVersion = "1.0"

	defaultWandbEntity  = "longti"
	defaultWandbProject = "inference"
)

// Telemetry wraps an optional OpenTelemetry trace provider used to export
// finite agent-processing turns to the W&B Weave Agents OTLP endpoint. When
// disabled (no endpoint configured), every method is a no-op so existing
// behavior is fully preserved.
type Telemetry struct {
	tp             *sdktrace.TracerProvider
	tracer         trace.Tracer
	agentName      string
	conversationID string
	enabled        bool
}

// StartTurn starts an invoke_agent span representing one finite
// agent-processing turn. The returned context carries the active span so
// nested chat and execute_tool spans become its children.
func (t *Telemetry) StartTurn(ctx context.Context) (context.Context, trace.Span) {
	if t == nil || !t.enabled || t.tracer == nil {
		return ctx, nil
	}
	ctx, span := t.tracer.Start(ctx, "invoke_agent "+t.agentName)
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.agent.name", t.agentName),
		attribute.String("gen_ai.conversation.id", t.conversationID),
	)
	return ctx, span
}

// StartChat starts a chat span nested under the active turn span.
func (t *Telemetry) StartChat(ctx context.Context, model string) (context.Context, trace.Span) {
	if t == nil || !t.enabled || t.tracer == nil {
		return ctx, nil
	}
	ctx, span := t.tracer.Start(ctx, "chat "+model)
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.conversation.id", t.conversationID),
		attribute.String("gen_ai.request.model", model),
	)
	return ctx, span
}

// StartTool starts an execute_tool span nested under the active turn span.
func (t *Telemetry) StartTool(ctx context.Context, name string) (context.Context, trace.Span) {
	if t == nil || !t.enabled || t.tracer == nil {
		return ctx, nil
	}
	ctx, span := t.tracer.Start(ctx, "execute_tool "+name)
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.conversation.id", t.conversationID),
		attribute.String("gen_ai.tool.name", name),
	)
	return ctx, span
}

// Shutdown flushes pending spans and shuts the provider down. The caller is
// responsible for supplying a bounded context.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil || !t.enabled || t.tp == nil {
		return nil
	}
	return t.tp.Shutdown(ctx)
}

// Enabled reports whether spans are being exported.
func (t *Telemetry) Enabled() bool {
	return t != nil && t.enabled
}

// newTelemetry creates the OTLP HTTP/protobuf trace provider using the full
// traces URL verbatim (no /v1/traces suffix is appended). It authenticates
// with the wandb-api-key header and routes spans via wandb.entity / wandb.project
// resource attributes. The API key is never logged.
func newTelemetry(cfg Config, wandbAPIKey, version, conversationID string) (*Telemetry, error) {
	endpoint := strings.TrimSpace(cfg.OTLPTracesEndpoint)
	if endpoint == "" {
		return &Telemetry{}, nil
	}

	entity, project := wandbProjectRouting(cfg)
	headers := map[string]string{"wandb-api-key": wandbAPIKey}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(10 * time.Second),
	}
	exporter, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("create otlp http exporter: %w", err)
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			attribute.String("wandb.entity", entity),
			attribute.String("wandb.project", project),
			attribute.String("service.name", traceServiceName),
			attribute.String("service.version", version),
			attribute.String("schema.version", traceSchemaVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build otlp resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(exporter)),
	)
	return &Telemetry{
		tp:             tp,
		tracer:         tp.Tracer(traceServiceName),
		agentName:      traceAgentName,
		conversationID: conversationID,
		enabled:        true,
	}, nil
}

// newTestTelemetry builds an enabled Telemetry around an externally supplied
// tracer. It is used by tests that install an in-memory exporter.
func newTestTelemetry(tracer trace.Tracer, agentName, conversationID string) *Telemetry {
	return &Telemetry{
		tracer:         tracer,
		agentName:      agentName,
		conversationID: conversationID,
		enabled:        true,
	}
}

// wandbProjectRouting resolves the entity/project pair from explicit config,
// then the OpenAI-Project header, then defaults.
func wandbProjectRouting(cfg Config) (entity, project string) {
	entity = strings.TrimSpace(cfg.WandbEntity)
	project = strings.TrimSpace(cfg.WandbProject)
	if entity == "" || project == "" {
		header := strings.TrimSpace(cfg.ProjectHeader)
		header = strings.TrimSpace(strings.TrimPrefix(header, "OpenAI-Project:"))
		if slash := strings.IndexByte(header, '/'); slash > 0 {
			e, p := strings.TrimSpace(header[:slash]), strings.TrimSpace(header[slash+1:])
			if entity == "" {
				entity = e
			}
			if project == "" {
				project = p
			}
		}
	}
	if entity == "" {
		entity = defaultWandbEntity
	}
	if project == "" {
		project = defaultWandbProject
	}
	return entity, project
}

// finishChatSpan records usage/outcome metadata on a chat span. Missing usage
// values are left unset rather than fabricated.
func finishChatSpan(span trace.Span, record LLMCallRecord) {
	if span == nil {
		return
	}
	var attrs []attribute.KeyValue
	if record.TotalTokens > 0 {
		attrs = append(attrs,
			attribute.Int64("gen_ai.usage.input_tokens", int64(record.PromptTokens)),
			attribute.Int64("gen_ai.usage.output_tokens", int64(record.CompletionTokens)),
			attribute.Int64("gen_ai.usage.total_tokens", int64(record.TotalTokens)),
		)
	}
	if record.FinishReason != "" {
		attrs = append(attrs, attribute.String("gen_ai.response.finish_reason", record.FinishReason))
	}
	if record.DurationMS > 0 {
		attrs = append(attrs, attribute.Int64("iterxp.duration_ms", record.DurationMS))
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	if record.Error != "" {
		span.SetStatus(codes.Error, "model_request_failed")
	} else {
		span.SetStatus(codes.Ok, "")
	}
}

// finishToolSpan records the bounded outcome class and exit code on an
// execute_tool span. Raw stdout/stderr/error text is never exported.
func finishToolSpan(span trace.Span, result ToolResult) {
	if span == nil {
		return
	}
	span.SetAttributes(attribute.Int64("iterxp.tool.exit_code", int64(result.ExitCode)))
	switch {
	case result.ExitCode == 124:
		span.SetStatus(codes.Error, "tool_timeout")
	case result.ExitCode != 0:
		span.SetStatus(codes.Error, "tool_non_zero_exit")
	case strings.TrimSpace(result.Error) != "":
		span.SetStatus(codes.Error, "tool_error")
	default:
		span.SetStatus(codes.Ok, "")
	}
}

// recordTrace persists the native trace/span IDs for the current turn in the
// in-memory session state, keeping issue/session-to-trace association local.
func recordTrace(st *SessionState, sc trace.SpanContext, operation string) {
	if st == nil || !sc.IsValid() {
		return
	}
	st.Traces = append(st.Traces, SessionTrace{
		TraceID:   sc.TraceID().String(),
		SpanID:    sc.SpanID().String(),
		Operation: operation,
		Timestamp: time.Now().UTC(),
	})
	if len(st.Traces) > 20 {
		st.Traces = st.Traces[len(st.Traces)-20:]
	}
}

// startTurn / startChat / startTool are thin Agent/Client entry points so the
// execution paths do not need to be aware of a nil telemetry instance.
func (a *Agent) startTurn(ctx context.Context) (context.Context, trace.Span) {
	if a == nil || a.telemetry == nil {
		return ctx, nil
	}
	return a.telemetry.StartTurn(ctx)
}

func (a *Agent) startTool(ctx context.Context, name string) (context.Context, trace.Span) {
	if a == nil || a.telemetry == nil {
		return ctx, nil
	}
	return a.telemetry.StartTool(ctx, name)
}

func (c *Client) startChat(ctx context.Context, model string) (context.Context, trace.Span) {
	if c == nil || c.telemetry == nil {
		return ctx, nil
	}
	return c.telemetry.StartChat(ctx, model)
}

// initWorkerTelemetry creates the worker's per-process trace provider. It reads
// the W&B API key from the already-constructed client (never printing it). When
// no endpoint is configured, newTelemetry returns a disabled no-op instance, so
// this is safe to call unconditionally. Export/configuration failures are
// returned as errors that the caller logs without failing the task.
func initWorkerTelemetry(cfg Config, client *Client, session *Session, version string) (*Telemetry, error) {
	if client == nil {
		return nil, fmt.Errorf("telemetry requires a client")
	}
	conversationID := issueConversationID(session)
	return newTelemetry(cfg, client.tokens["wandb"], version, conversationID)
}

// telemetryShutdown flushes pending spans with a bounded context so a hung
// exporter cannot block worker exit.
func telemetryShutdown(t *Telemetry) {
	if t == nil || !t.enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = t.Shutdown(ctx)
}

// issueConversationID derives a stable conversation ID for an issue session so
// turns for the same issue group together in the Weave Agents view.
func issueConversationID(session *Session) string {
	if session == nil {
		return traceServiceName + "-conversation"
	}
	return fmt.Sprintf("%s-issue-%d", traceServiceName, session.IssueNumber)
}
