package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func attrMap(spans tracetest.SpanStubs, name string) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, s := range spans {
		for _, a := range s.Attributes {
			if string(a.Key) == name {
				out[string(a.Key)] = a.Value
			}
		}
	}
	return out
}

func spanByAttr(spans tracetest.SpanStubs, key, value string) tracetest.SpanStub {
	for _, s := range spans {
		for _, a := range s.Attributes {
			if string(a.Key) == key && a.Value.AsString() == value {
				return s
			}
		}
	}
	return tracetest.SpanStub{}
}

func newRecordingProvider(t *testing.T) (*Telemetry, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("wandb.entity", defaultWandbEntity),
			attribute.String("wandb.project", defaultWandbProject),
		)),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tel := newTestTelemetry(tp.Tracer("test-tracer"), "test-agent", "conv-1")
	return tel, exp
}

func TestWandbProjectRouting(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantEntity  string
		wantProject string
	}{
		{name: "explicit", cfg: Config{WandbEntity: "acme", WandbProject: "widgets"}, wantEntity: "acme", wantProject: "widgets"},
		{name: "header only", cfg: Config{ProjectHeader: "OpenAI-Project: myteam/myproj"}, wantEntity: "myteam", wantProject: "myproj"},
		{name: "defaults", cfg: Config{}, wantEntity: "longti", wantProject: "inference"},
		{name: "partial explicit entity", cfg: Config{WandbEntity: "acme", ProjectHeader: "OpenAI-Project: myteam/myproj"}, wantEntity: "acme", wantProject: "myproj"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p := wandbProjectRouting(tt.cfg)
			if e != tt.wantEntity || p != tt.wantProject {
				t.Fatalf("wandbProjectRouting = (%q,%q), want (%q,%q)", e, p, tt.wantEntity, tt.wantProject)
			}
		})
	}
}

func TestTelemetryDisabledWithoutEndpoint(t *testing.T) {
	tel, err := newTelemetry(Config{}, "secret-key", "v1", "conv-1")
	if err != nil {
		t.Fatalf("newTelemetry disabled: %v", err)
	}
	if tel.Enabled() {
		t.Fatalf("telemetry should be disabled without an endpoint")
	}
	ctx, span := tel.StartTurn(context.Background())
	if span != nil {
		t.Fatalf("disabled telemetry should return a nil span")
	}
	if ctx == nil {
		t.Fatalf("disabled telemetry should return the input context")
	}
}

func TestParentChildSpansAndAttributes(t *testing.T) {
	tel, exp := newRecordingProvider(t)

	ctx, turn := tel.StartTurn(context.Background())
	if turn == nil {
		t.Fatal("turn span is nil")
	}
	if !turn.SpanContext().IsValid() {
		t.Fatal("turn span context is invalid")
	}

	_, chat := tel.StartChat(ctx, "model-x")
	if chat == nil {
		t.Fatal("chat span is nil")
	}

	_, tool := tel.StartTool(ctx, "bash")
	if tool == nil {
		t.Fatal("tool span is nil")
	}

	_ = chat
	_ = tool

	// End in reverse order.
	tool.End()
	chat.End()
	turn.End()

	spans := exp.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("span count = %d, want 3", len(spans))
	}

	byOp := map[string]tracetest.SpanStub{}
	for _, s := range spans {
		for _, a := range s.Attributes {
			if string(a.Key) == "gen_ai.operation.name" {
				byOp[a.Value.AsString()] = s
			}
		}
	}

	turnSpan, ok := byOp["invoke_agent"]
	if !ok {
		t.Fatalf("missing invoke_agent span; attrs=%+v", spans)
	}
	chatSpan, ok := byOp["chat"]
	if !ok {
		t.Fatalf("missing chat span")
	}
	toolSpan, ok := byOp["execute_tool"]
	if !ok {
		t.Fatalf("missing execute_tool span")
	}

	// Parent/child: chat and tool must share the turn trace ID and be children.
	if chatSpan.SpanContext.TraceID() != turnSpan.SpanContext.TraceID() {
		t.Fatalf("chat trace id = %s, want turn trace id %s", chatSpan.SpanContext.TraceID(), turnSpan.SpanContext.TraceID())
	}
	if toolSpan.SpanContext.TraceID() != turnSpan.SpanContext.TraceID() {
		t.Fatalf("tool trace id = %s, want turn trace id %s", toolSpan.SpanContext.TraceID(), turnSpan.SpanContext.TraceID())
	}
	if chatSpan.Parent.SpanID() != turnSpan.SpanContext.SpanID() {
		t.Fatalf("chat parent span id mismatch")
	}
	if toolSpan.Parent.SpanID() != turnSpan.SpanContext.SpanID() {
		t.Fatalf("tool parent span id mismatch")
	}

	// Model name recorded on chat.
	if got := spanAttrs(chatSpan)["gen_ai.request.model"].AsString(); got != "model-x" {
		t.Fatalf("chat model = %q, want model-x", got)
	}
	// Tool name recorded.
	if got := spanAttrs(toolSpan)["gen_ai.tool.name"].AsString(); got != "bash" {
		t.Fatalf("tool name = %q, want bash", got)
	}
	// Conversation ID on turn.
	if got := spanAttrs(turnSpan)["gen_ai.conversation.id"].AsString(); got != "conv-1" {
		t.Fatalf("conversation id = %q, want conv-1", got)
	}
}

func spanAttrs(s tracetest.SpanStub) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	for _, a := range s.Attributes {
		out[string(a.Key)] = a.Value
	}
	return out
}

func TestFinishChatSpanRecordsUsageAndFinishNotSecrets(t *testing.T) {
	tel, exp := newRecordingProvider(t)
	_, chat := tel.StartChat(context.Background(), "model-x")
	finishChatSpan(chat, LLMCallRecord{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		FinishReason:     "stop",
		DurationMS:       42,
		Error:            "",
	})
	chat.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1", len(spans))
	}
	attrs := spanAttrs(spans[0])
	if int64(attrs["gen_ai.usage.input_tokens"].AsInt64()) != 10 {
		t.Fatalf("input tokens = %v", attrs["gen_ai.usage.input_tokens"])
	}
	if int64(attrs["gen_ai.usage.output_tokens"].AsInt64()) != 5 {
		t.Fatalf("output tokens = %v", attrs["gen_ai.usage.output_tokens"])
	}
	if attrs["gen_ai.response.finish_reason"].AsString() != "stop" {
		t.Fatalf("finish reason = %q", attrs["gen_ai.response.finish_reason"])
	}
	// No secret or content-like attribute names may appear.
	for _, a := range spans[0].Attributes {
		k := strings.ToLower(string(a.Key))
		if strings.Contains(k, "key") || strings.Contains(k, "secret") || strings.Contains(k, "prompt") || strings.Contains(k, "content") {
			t.Fatalf("unexpected sensitive attribute %q", a.Key)
		}
	}
	if spans[0].Status.Code != codes.Ok {
		t.Fatalf("status = %v, want Ok", spans[0].Status.Code)
	}
}

func TestFinishChatSpanErrorStatus(t *testing.T) {
	tel, exp := newRecordingProvider(t)
	_, chat := tel.StartChat(context.Background(), "model-x")
	finishChatSpan(chat, LLMCallRecord{Error: "boom"})
	chat.End()
	spans := exp.GetSpans()
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("status = %v, want Error", spans[0].Status.Code)
	}
}

func TestFinishToolSpanOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		result     ToolResult
		wantCode   codes.Code
		wantStatus string
	}{
		{name: "ok", result: ToolResult{ExitCode: 0}, wantCode: codes.Ok, wantStatus: ""},
		{name: "non-zero", result: ToolResult{ExitCode: 3}, wantCode: codes.Error, wantStatus: "tool_non_zero_exit"},
		{name: "timeout", result: ToolResult{ExitCode: 124}, wantCode: codes.Error, wantStatus: "tool_timeout"},
		{name: "tool error text", result: ToolResult{ExitCode: 0, Error: "failed"}, wantCode: codes.Error, wantStatus: "tool_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel, exp := newRecordingProvider(t)
			_, tool := tel.StartTool(context.Background(), "bash")
			finishToolSpan(tool, tt.result)
			tool.End()
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("span count = %d", len(spans))
			}
			if spans[0].Status.Code != tt.wantCode {
				t.Fatalf("status code = %v, want %v", spans[0].Status.Code, tt.wantCode)
			}
			if tt.wantStatus != "" && spans[0].Status.Description != tt.wantStatus {
				t.Fatalf("status description = %q, want %q", spans[0].Status.Description, tt.wantStatus)
			}
			// Raw output must not be exported.
			for _, a := range spans[0].Attributes {
				if strings.Contains(string(a.Key), "stdout") || strings.Contains(string(a.Key), "stderr") {
					t.Fatalf("raw tool output attribute exported: %q", a.Key)
				}
			}
			if got := spanAttrs(spans[0])["iterxp.tool.exit_code"].AsInt64(); got != int64(tt.result.ExitCode) {
				t.Fatalf("exit code attr = %d, want %d", got, tt.result.ExitCode)
			}
		})
	}
}

func TestRecordTracePersistsBounded(t *testing.T) {
	st := &SessionState{}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:  trace.SpanID{9, 9, 9, 9, 9, 9, 9, 9},
	})
	for i := 0; i < 30; i++ {
		recordTrace(st, sc, "invoke_agent")
	}
	if len(st.Traces) != 20 {
		t.Fatalf("traces length = %d, want 20 (bounded)", len(st.Traces))
	}
	for _, tr := range st.Traces {
		if tr.TraceID != sc.TraceID().String() || tr.SpanID != sc.SpanID().String() {
			t.Fatalf("trace id not persisted: %+v", tr)
		}
		if tr.Operation != "invoke_agent" {
			t.Fatalf("operation = %q", tr.Operation)
		}
		if tr.Timestamp.IsZero() {
			t.Fatalf("timestamp missing")
		}
	}
}

func TestFinishToolSpanNilSpanIsSafe(t *testing.T) {
	finishToolSpan(nil, ToolResult{ExitCode: 1})
	recordTrace(&SessionState{}, trace.SpanContext{}, "invoke_agent")
}

func TestStartTurnNoopWhenNil(t *testing.T) {
	var tel *Telemetry
	ctx, span := tel.StartTurn(context.Background())
	if span != nil {
		t.Fatalf("nil telemetry should return nil span")
	}
	if ctx == nil {
		t.Fatalf("nil telemetry should return input context")
	}
}

func TestNewTelemetryExportsProtobufWithAuthHeader(t *testing.T) {
	var gotCT string
	var gotAuth string
	var gotPath string
	var gotBody int64

	srv := newTLSTestServer(t, func(headers map[string][]string, path string, body []byte) {
		gotPath = path
		gotBody = int64(len(body))
		for k, v := range headers {
			if strings.EqualFold(k, "Content-Type") && len(v) > 0 {
				gotCT = v[0]
			}
			if strings.EqualFold(k, "Wandb-Api-Key") && len(v) > 0 {
				gotAuth = v[0]
			}
		}
	})
	defer srv.Close()

	tel, err := newTelemetry(Config{
		OTLPTracesEndpoint: srv.URL + "/agents/otel/v1/traces",
		WandbEntity:        "acme",
		WandbProject:       "widgets",
	}, "supersecretkey", "v1", "conv-1")
	if err != nil {
		t.Fatalf("newTelemetry: %v", err)
	}
	defer tel.Shutdown(context.Background())

	_, turn := tel.StartTurn(context.Background())
	if turn == nil {
		t.Fatal("turn nil")
	}
	turn.End()

	// Trigger export synchronously via ForceFlush then Shutdown.
	if err := tel.tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("force flush: %v", err)
	}

	if gotBody == 0 {
		t.Fatalf("no body exported")
	}
	if gotPath != "/agents/otel/v1/traces" {
		t.Fatalf("export path = %q, want /agents/otel/v1/traces", gotPath)
	}
	if !strings.Contains(gotCT, "application/x-protobuf") {
		t.Fatalf("content-type = %q, want protobuf", gotCT)
	}
	if gotAuth != "supersecretkey" {
		t.Fatalf("wandb-api-key header not set correctly")
	}
}

func newTLSTestServer(t *testing.T, handler func(map[string][]string, string, []byte)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := map[string][]string{}
		for k, v := range r.Header {
			headers[k] = v
		}
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		handler(headers, r.URL.Path, body)
		w.WriteHeader(http.StatusOK)
	}))
	return srv
}

func TestAgentEnvFallbackAndProcessEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ITERXP_CONFIG_DIR", dir)
	t.Setenv("ITERXP_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("ITERXP_WANDB_ENTITY", "")
	t.Setenv("ITERXP_WANDB_PROJECT", "")

	if err := os.WriteFile(filepath.Join(dir, "agent.env"), []byte("ITERXP_OTLP_TRACES_ENDPOINT=https://trace.example/agents/otel/v1/traces\nITERXP_WANDB_ENTITY=acme\nITERXP_WANDB_PROJECT=widgets\n# comment line\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig()
	if cfg.OTLPTracesEndpoint != "https://trace.example/agents/otel/v1/traces" {
		t.Fatalf("endpoint from file = %q", cfg.OTLPTracesEndpoint)
	}
	if cfg.WandbEntity != "acme" || cfg.WandbProject != "widgets" {
		t.Fatalf("entity/project from file = %q/%q", cfg.WandbEntity, cfg.WandbProject)
	}

	// Process env wins over file.
	t.Setenv("ITERXP_WANDB_ENTITY", "shell-entity")
	cfg = loadConfig()
	if cfg.WandbEntity != "shell-entity" {
		t.Fatalf("entity = %q, want shell-entity", cfg.WandbEntity)
	}
}
