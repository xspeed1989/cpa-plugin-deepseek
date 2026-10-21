// Package deepseekcpa implements the deepseek-cpa provider plugin for
// CLIProxyAPI. It serves DeepSeek's three protocol surfaces by forwarding the
// client's original payload to the matching DeepSeek endpoint, without
// translating between protocols. See docs/adr/0001.
package deepseekcpa

import (
	"context"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pluginVersion is the release version reported at registration.
var pluginVersion = "0.4.6"

// Version returns the plugin release version reported to the host.
func Version() string { return pluginVersion }

// Plugin implements every capability this plugin declares. One value backs all
// of them so the ABI layer keeps a single pointer.
type Plugin struct {
	bridge HostBridge

	modelMu sync.Mutex
	models  []ModelEntry
}

// Build validates plugins.configs.deepseek-cpa and describes the plugin to the
// host. It fails fast: an invalid configuration must not register a half-usable
// provider.
func Build(configYAML []byte, bridge HostBridge) (pluginapi.Plugin, *Plugin, error) {
	if bridge == nil {
		return pluginapi.Plugin{}, nil, fmt.Errorf("build: host bridge is required")
	}
	if err := validateConfig(configYAML); err != nil {
		return pluginapi.Plugin{}, nil, err
	}
	p := &Plugin{bridge: bridge}
	if err := p.bridge.Log(context.Background(), "info", "deepseek-cpa registered", nil); err != nil {
		return pluginapi.Plugin{}, nil, fmt.Errorf("build: host log: %w", err)
	}

	return pluginapi.Plugin{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             Provider,
			Version:          pluginVersion,
			Author:           "xspeed1989",
			GitHubRepository: "https://github.com/xspeed1989/cpa-plugin-deepseek",
		},
		Capabilities: pluginapi.Capabilities{
			ModelProvider:      p,
			AuthProvider:       p,
			Executor:           p,
			ExecutorModelScope: pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats: []string{
				"chat-completions", "responses", "anthropic",
			},
			ExecutorOutputFormats: []string{
				"chat-completions", "responses", "anthropic",
			},
			UsagePlugin:   p,
			ManagementAPI: p,
		},
	}, p, nil
}

// Identifier returns the provider key handled by every capability.
func (p *Plugin) Identifier() string { return Provider }

// StaticModels publishes the model list under the plugin namespace.
//
// A successful probe is the source of truth. Until one succeeds the built-in
// bootstrap list answers, and a failed probe never fails registration: the
// plugin must load even when DeepSeek is unreachable or no credential exists
// yet. The first call has no credential, so a fresh install does no network I/O.
func (p *Plugin) StaticModels(ctx context.Context, _ pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	if cached := p.cachedModels(); len(cached) > 0 {
		return pluginapi.ModelResponse{Provider: Provider, Models: publishModels(cached)}, nil
	}

	entries, err := p.probeModels(ctx)
	if err != nil {
		// The fallback log is the last diagnostic before the bootstrap list, and
		// failing registration because the logger failed would break the promise
		// that a failed probe never blocks the plugin from loading.
		_ = p.bridge.Log(ctx, "info", "serving bootstrap models", map[string]any{"reason": err.Error()})
		return pluginapi.ModelResponse{Provider: Provider, Models: publishModels(defaultModels())}, nil
	}
	p.setModels(entries)
	return pluginapi.ModelResponse{Provider: Provider, Models: publishModels(entries)}, nil
}

// ModelsForAuth is part of the model provider capability but unused: the plugin
// registers static models only (executor_model_scope: static).
func (p *Plugin) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{}, notImplemented("model.for_auth")
}

// StartLogin reports that DeepSeek keys have no interactive login flow. This is
// permanent, not a gap: a DeepSeek key is a static string with no OAuth or
// device-code exchange to run.
func (p *Plugin) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, unsupported("auth.login.start", "DeepSeek API keys are static and have no interactive login flow")
}

// PollLogin reports that DeepSeek keys have no interactive login flow.
func (p *Plugin) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, unsupported("auth.login.poll", "DeepSeek API keys are static and have no interactive login flow")
}

// RefreshAuth reports that DeepSeek keys never expire, so there is nothing to
// refresh.
func (p *Plugin) RefreshAuth(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{}, unsupported("auth.refresh", "DeepSeek API keys do not expire, so there is nothing to refresh")
}

// CountTokens is not backed by an upstream endpoint.
func (p *Plugin) CountTokens(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, notImplemented("executor.count_tokens")
}

// HttpRequest bridges executor-owned raw HTTP through the host client.
func (p *Plugin) HttpRequest(context.Context, pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return pluginapi.ExecutorHTTPResponse{}, notImplemented("executor.http_request")
}

func notImplemented(method string) error {
	return fmt.Errorf("%s: %w", method, ErrNotImplemented)
}

func unsupported(method, reason string) error {
	return fmt.Errorf("%s: %w: %s", method, ErrUnsupported, reason)
}

var (
	_ pluginapi.AuthProvider      = (*Plugin)(nil)
	_ pluginapi.ModelProvider     = (*Plugin)(nil)
	_ pluginapi.ProviderExecutor  = (*Plugin)(nil)
	_ pluginapi.UsagePlugin       = (*Plugin)(nil)
	_ pluginapi.ManagementAPI     = (*Plugin)(nil)
	_ pluginapi.ManagementHandler = (*Plugin)(nil)
)
