package deepseekcpa

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/sjson"
)

// Protocol format tokens used by the host (see docs/adr/0001). The plugin
// declares all three, so a request never needs translating.
const (
	formatChat      = "openai"
	formatResponses = "openai-response"
	formatAnthropic = "claude"
)

// DeepSeek endpoints, one per protocol surface. Fixed by the spec: the plugin
// only talks to the official DeepSeek API.
const (
	chatEndpoint      = "https://api.deepseek.com/chat/completions"
	responsesEndpoint = "https://api.deepseek.com/responses"
	anthropicEndpoint = "https://api.deepseek.com/anthropic/v1/messages"
)

// headersNeverForwarded carry the client's own proxy credential, so forwarding
// them would hand our caller's key to DeepSeek or corrupt the request framing,
// plus the RFC 7230 hop-by-hop headers. An edge proxy in front of the host can
// hand us "Connection: upgrade"; forwarding it makes Go's HTTP/2 client reject
// the whole request before it leaves the process.
var headersNeverForwarded = map[string]struct{}{
	"Authorization":       {},
	"X-Api-Key":           {},
	"Host":                {},
	"Content-Length":      {},
	"Accept-Encoding":     {},
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Proxy-Connection":    {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

// upstreamCall is everything a request needs before it goes to DeepSeek: which
// endpoint, the client's protocol, and the body/headers to send.
type upstreamCall struct {
	endpoint string
	format   string
	headers  http.Header
	body     []byte
}

// prepareCall resolves the endpoint, rewrites the model name, and attaches the
// host-managed credential. Both the streaming and non-streaming paths share it.
func (p *Plugin) prepareCall(req pluginapi.ExecutorRequest) (upstreamCall, error) {
	endpoint, err := endpointForFormat(req.SourceFormat)
	if err != nil {
		return upstreamCall{}, err
	}
	upstreamModel, err := upstreamModelName(req.Model)
	if err != nil {
		return upstreamCall{}, err
	}
	apiKey, err := credentialAPIKey(req.StorageJSON)
	if err != nil {
		return upstreamCall{}, err
	}
	body, err := sjson.SetBytes(req.Payload, "model", upstreamModel)
	if err != nil {
		return upstreamCall{}, fmt.Errorf("executor: rewrite model: %w", err)
	}

	headers := forwardedHeaders(req.Headers)
	headers.Set("Authorization", "Bearer "+apiKey)

	return upstreamCall{
		endpoint: endpoint,
		format:   strings.TrimSpace(req.SourceFormat),
		headers:  headers,
		body:     body,
	}, nil
}

// Execute forwards one non-streaming request to the DeepSeek endpoint for the
// client's protocol surface. The body goes upstream unchanged except for the
// model name; DeepSeek is stateless across all three surfaces.
func (p *Plugin) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	call, err := p.prepareCall(req)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}

	upstream, err := p.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     call.endpoint,
		Headers: call.headers,
		Body:    call.body,
	})
	if err != nil {
		return pluginapi.ExecutorResponse{}, fmt.Errorf("executor.execute: %w", err)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode > 299 {
		return pluginapi.ExecutorResponse{}, &upstreamError{
			statusCode: upstream.StatusCode,
			body:       upstream.Body,
		}
	}
	return pluginapi.ExecutorResponse{Payload: upstream.Body, Headers: upstream.Headers}, nil
}

// upstreamError carries the upstream status and body back to the client.
//
// Plugin errors cross the RPC boundary as JSON, so the body travels as the
// error message: the host compacts a valid JSON message and returns it with the
// status from the envelope's http_status, which is exactly the passthrough the
// spec asks for. A wrapped or summarised message would lose the upstream shape.
type upstreamError struct {
	statusCode int
	body       []byte
	// cause explains the failure when the upstream sent no usable body.
	cause error
}

func (e *upstreamError) Error() string {
	if len(e.body) > 0 {
		return string(e.body)
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return http.StatusText(e.statusCode)
}

func (e *upstreamError) StatusCode() int { return e.statusCode }

// endpointForFormat maps the client's protocol to its DeepSeek endpoint. The
// host always fills SourceFormat before calling an executor; an empty one means
// the request never named a protocol, so guessing would pick the wrong surface.
func endpointForFormat(rawFormat string) (string, error) {
	format := strings.TrimSpace(rawFormat)
	switch format {
	case formatChat:
		return chatEndpoint, nil
	case formatResponses:
		return responsesEndpoint, nil
	case formatAnthropic:
		return anthropicEndpoint, nil
	case "":
		return "", fmt.Errorf("executor: request carries no source protocol format")
	default:
		return "", fmt.Errorf("executor: unsupported protocol format %q", format)
	}
}

// upstreamModelName strips the plugin namespace prefix. A model without it does
// not belong to this plugin, so sending it upstream would silently misroute.
func upstreamModelName(model string) (string, error) {
	trimmed := strings.TrimSpace(model)
	prefix := Provider + "/"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", fmt.Errorf("executor: model %q is missing the required %q prefix", trimmed, prefix)
	}
	name := strings.TrimPrefix(trimmed, prefix)
	if name == "" {
		return "", fmt.Errorf("executor: model %q has an empty upstream name", trimmed)
	}
	return name, nil
}

// credentialAPIKey reads the upstream key out of the host-managed credential.
func credentialAPIKey(storageJSON []byte) (string, error) {
	credential, ours, err := decodeCredential(storageJSON)
	if err != nil {
		return "", fmt.Errorf("executor: %w", err)
	}
	if !ours {
		return "", fmt.Errorf("executor: credential does not declare provider %q", Provider)
	}
	if strings.TrimSpace(credential.APIKey) == "" {
		return "", fmt.Errorf("executor: credential is missing api_key")
	}
	return strings.TrimSpace(credential.APIKey), nil
}

// forwardedHeaders keeps the client headers that describe the protocol while
// dropping the ones that carry the client's own credential or framing.
func forwardedHeaders(client http.Header) http.Header {
	out := make(http.Header, len(client)+2)
	for name, values := range client {
		if _, blocked := headersNeverForwarded[http.CanonicalHeaderKey(name)]; blocked {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	if out.Get("Content-Type") == "" {
		out.Set("Content-Type", "application/json")
	}
	return out
}
