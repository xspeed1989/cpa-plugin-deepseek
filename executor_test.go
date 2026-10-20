package deepseekcpa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func executeRequest(model, payload, format string) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{
		AuthID:       "auth-1",
		AuthProvider: Provider,
		Model:        model,
		Format:       format,
		SourceFormat: format,
		Payload:      []byte(payload),
		StorageJSON:  []byte(`{"type":"deepseek-cpa","api_key":"sk-upstream"}`),
	}
}

func TestExecuteForwardsEachProtocolSurfaceVerbatim(t *testing.T) {
	// Deliberately awkward formatting: key order, spacing and unknown fields
	// must survive untouched on every surface.
	cases := []struct {
		name    string
		format  string
		payload string
		wantURL string
	}{
		{
			name:   "responses",
			format: formatResponses,
			payload: `{ "model" : "deepseek-cpa/deepseek-v4-pro",
  "input" : [ {"role":"user","content":"hi"} ] ,
  "reasoning": {"effort":"high"}, "unknown_field": [1, 2.50, true, null] }`,
			wantURL: "https://api.deepseek.com/responses",
		},
		{
			name:    "chat completions",
			format:  formatChat,
			payload: `{ "model" : "deepseek-cpa/deepseek-flash", "messages" : [ {"role":"user","content":"hi"} ] }`,
			wantURL: "https://api.deepseek.com/chat/completions",
		},
		{
			name:    "anthropic",
			format:  formatAnthropic,
			payload: `{ "model" : "deepseek-cpa/deepseek-v4-pro", "system" : "be brief", "max_tokens" : 16, "messages" : [] }`,
			wantURL: "https://api.deepseek.com/anthropic/v1/messages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &fakeBridge{doResponse: pluginapi.HTTPResponse{
				StatusCode: 200,
				Headers:    http.Header{"Content-Type": {"application/json"}},
				Body:       []byte(`{"id":"ok"}`),
			}}
			plugin := newManagementPlugin(t, bridge)

			// The client model and the body model agree, as they do in a real
			// request; req.Model is the routing truth.
			model := modelInPayload(t, tc.payload)
			resp, err := plugin.Execute(context.Background(),
				executeRequest(model, tc.payload, tc.format))
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if string(resp.Payload) != `{"id":"ok"}` {
				t.Errorf("Payload = %q, want the upstream body verbatim", resp.Payload)
			}

			if len(bridge.requests) != 1 {
				t.Fatalf("upstream requests = %d, want 1", len(bridge.requests))
			}
			sent := bridge.requests[0]
			if sent.Method != http.MethodPost {
				t.Errorf("Method = %q, want POST", sent.Method)
			}
			if sent.URL != tc.wantURL {
				t.Errorf("URL = %q, want %q", sent.URL, tc.wantURL)
			}
			if got := sent.Headers.Get("Authorization"); got != "Bearer sk-upstream" {
				t.Errorf("Authorization = %q, want the credential key", got)
			}

			want := strings.Replace(tc.payload, model, strings.TrimPrefix(model, Provider+"/"), 1)
			if string(sent.Body) != want {
				t.Errorf("upstream body =\n%s\nwant\n%s", sent.Body, want)
			}
		})
	}
}

// modelInPayload reads the model the client sent, so the expectation is
// derived from the request rather than from the code under test.
func modelInPayload(t *testing.T, payload string) string {
	t.Helper()
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("test payload is not valid JSON: %v", err)
	}
	return parsed.Model
}

func TestExecuteKeepsContextLengthSuffix(t *testing.T) {
	bridge := &fakeBridge{doResponse: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{}`)}}
	plugin := newManagementPlugin(t, bridge)

	_, err := plugin.Execute(context.Background(),
		executeRequest(Provider+"/deepseek-v4-pro[1m]", `{"model":"`+Provider+`/deepseek-v4-pro[1m]"}`, formatResponses))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(bridge.requests[0].Body); got != `{"model":"deepseek-v4-pro[1m]"}` {
		t.Errorf("upstream body = %q, want the [1m] suffix preserved", got)
	}
}

func TestExecuteNeverForwardsTheClientCredential(t *testing.T) {
	bridge := &fakeBridge{doResponse: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{}`)}}
	plugin := newManagementPlugin(t, bridge)

	req := executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, formatResponses)
	req.Headers = http.Header{
		"Authorization": {"Bearer proxy-client-key"},
		"X-Api-Key":     {"client-key"},
		"X-Trace":       {"keep-me"},
	}

	if _, err := plugin.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	sent := bridge.requests[0].Headers
	if got := sent.Get("Authorization"); got != "Bearer sk-upstream" {
		t.Errorf("Authorization = %q, want only the upstream credential", got)
	}
	if got := sent.Get("X-Api-Key"); got != "" {
		t.Errorf("X-Api-Key = %q, want client credentials dropped", got)
	}
	if got := sent.Get("X-Trace"); got != "keep-me" {
		t.Errorf("X-Trace = %q, want harmless client headers forwarded", got)
	}
}

func TestExecuteDropsHopByHopHeaders(t *testing.T) {
	// An edge proxy can hand us Connection: upgrade; forwarding it makes Go's
	// HTTP/2 client refuse the request ("invalid Connection request header").
	bridge := &fakeBridge{doResponse: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{}`)}}
	plugin := newManagementPlugin(t, bridge)

	req := executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, formatResponses)
	req.Headers = http.Header{
		"Connection":        {"upgrade"},
		"Upgrade":           {"websocket"},
		"Keep-Alive":        {"timeout=5"},
		"Proxy-Connection":  {"keep-alive"},
		"Te":                {"trailers"},
		"Trailer":           {"X-Checksum"},
		"Transfer-Encoding": {"chunked"},
		"X-Trace":           {"keep-me"},
	}

	if _, err := plugin.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	sent := bridge.requests[0].Headers
	for _, name := range []string{"Connection", "Upgrade", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding"} {
		if got := sent.Values(name); len(got) != 0 {
			t.Errorf("%s = %q, want hop-by-hop headers dropped", name, got)
		}
	}
	if got := sent.Get("X-Trace"); got != "keep-me" {
		t.Errorf("X-Trace = %q, want end-to-end headers forwarded", got)
	}
}

func TestExecutePassesUpstreamStatusThrough(t *testing.T) {
	const body = `{"error":{"message":"rate limited","type":"rate_limit"}}`
	bridge := &fakeBridge{doResponse: pluginapi.HTTPResponse{StatusCode: 429, Body: []byte(body)}}
	plugin := newManagementPlugin(t, bridge)

	_, err := plugin.Execute(context.Background(),
		executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, formatResponses))
	if err == nil {
		t.Fatal("Execute() error = nil, want the upstream failure")
	}

	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 429 {
		t.Fatalf("error = %v, want StatusCode() == 429", err)
	}
	// The body travels as the error message: the RPC layer puts it in the error
	// envelope so the host can hand the upstream payload to the client.
	if err.Error() != body {
		t.Errorf("error message = %q, want the upstream body verbatim", err.Error())
	}
}

func TestExecuteRejectsUnprefixedModel(t *testing.T) {
	bridge := &fakeBridge{}
	plugin := newManagementPlugin(t, bridge)

	_, err := plugin.Execute(context.Background(),
		executeRequest("deepseek-v4-pro", `{"model":"deepseek-v4-pro"}`, formatResponses))
	if err == nil {
		t.Fatal("Execute() error = nil, want a missing-prefix error")
	}
	if !strings.Contains(err.Error(), Provider+"/") {
		t.Errorf("error = %q, want it to name the required prefix", err.Error())
	}
	if len(bridge.requests) != 0 {
		t.Error("upstream was called for a model this plugin does not own")
	}
}

func TestExecuteRejectsCredentialWithoutKey(t *testing.T) {
	bridge := &fakeBridge{}
	plugin := newManagementPlugin(t, bridge)

	req := executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, formatResponses)
	req.StorageJSON = []byte(`{"type":"deepseek-cpa"}`)

	_, err := plugin.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("Execute() error = nil, want a missing-credential error")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error = %q, want it to name api_key", err.Error())
	}
	if len(bridge.requests) != 0 {
		t.Error("upstream was called without a credential")
	}
}

func TestExecuteRejectsMissingSourceFormat(t *testing.T) {
	bridge := &fakeBridge{}
	plugin := newManagementPlugin(t, bridge)

	req := executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, formatResponses)
	req.SourceFormat = ""

	_, err := plugin.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("Execute() error = nil, want a missing-protocol error rather than a guess")
	}
	if len(bridge.requests) != 0 {
		t.Error("upstream was called without a known protocol")
	}
}

func TestExecuteRejectsUnsupportedFormat(t *testing.T) {
	bridge := &fakeBridge{}
	plugin := newManagementPlugin(t, bridge)

	_, err := plugin.Execute(context.Background(),
		executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, "gemini"))
	if err == nil {
		t.Fatal("Execute() error = nil, want an unsupported-format error")
	}
	if len(bridge.requests) != 0 {
		t.Error("upstream was called for an unsupported protocol")
	}
}

func TestExecutePropagatesTransportFailure(t *testing.T) {
	transportErr := errors.New("dial timeout")
	bridge := &fakeBridge{doErr: transportErr}
	plugin := newManagementPlugin(t, bridge)

	_, err := plugin.Execute(context.Background(),
		executeRequest(Provider+"/deepseek-v4-pro", `{"model":"x"}`, formatResponses))
	if !errors.Is(err, transportErr) {
		t.Fatalf("Execute() error = %v, want the transport error", err)
	}
}

// claudeCodePayload is the shape Claude Code sends on a second turn: thinking
// blocks and tool_use/tool_result history. A client resends that history on
// every turn, so the plugin must forward it byte for byte -- dropping a block
// would change the conversation the model sees.
const claudeCodePayload = `{"model":"deepseek-cpa/deepseek-v4-pro","max_tokens":4096,
 "system":[{"type":"text","text":"You are Claude Code"}],
 "thinking":{"type":"enabled","budget_tokens":4096},
 "tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
 "messages":[
  {"role":"user","content":[{"type":"text","text":"read a.txt"}]},
  {"role":"assistant","content":[
    {"type":"thinking","thinking":"I should read the file first.","signature":""},
    {"type":"text","text":"Reading it."},
    {"type":"tool_use","id":"toolu_01","name":"Read","input":{"path":"a.txt"}}]},
  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"hello"}]}]}`

func TestExecuteReplaysAnthropicToolHistoryVerbatim(t *testing.T) {
	bridge := &fakeBridge{doResponse: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{}`)}}
	plugin := newManagementPlugin(t, bridge)

	req := executeRequest(Provider+"/deepseek-v4-pro", claudeCodePayload, formatAnthropic)
	req.Headers = http.Header{"Anthropic-Version": {"2023-06-01"}}

	if _, err := plugin.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	sent := bridge.requests[0]
	if sent.URL != "https://api.deepseek.com/anthropic/v1/messages" {
		t.Errorf("URL = %q, want the anthropic endpoint", sent.URL)
	}
	want := strings.Replace(claudeCodePayload, Provider+"/deepseek-v4-pro", "deepseek-v4-pro", 1)
	if string(sent.Body) != want {
		t.Errorf("upstream body changed.\ngot:  %s\nwant: %s", sent.Body, want)
	}
	if got := sent.Headers.Get("Anthropic-Version"); got != "2023-06-01" {
		t.Errorf("Anthropic-Version = %q, want the client header forwarded", got)
	}
	if !strings.Contains(string(sent.Body), `"type":"thinking"`) || !strings.Contains(string(sent.Body), `"type":"tool_result"`) {
		t.Error("thinking/tool history was dropped from the forwarded body")
	}
}
