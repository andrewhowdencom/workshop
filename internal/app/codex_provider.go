package app

import (
	"context"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
)

// codexCompatibilityProvider removes request controls rejected by the ChatGPT
// Codex backend before delegating to ore's reusable Responses transport.
// Keeping this policy at the provider boundary prevents provider-agnostic paths
// such as compaction from needing Codex-specific behavior.
type codexCompatibilityProvider struct {
	inner provider.Provider
}

func (p *codexCompatibilityProvider) Invoke(
	ctx context.Context,
	state ledger.State,
	spec models.Spec,
	ch chan<- artifact.Artifact,
	opts ...provider.InvokeOption,
) error {
	spec.MaxOutputTokens = 0
	spec.Temperature = nil
	spec.TopP = nil
	spec.TopK = nil
	spec.Seed = nil
	spec.StopSequences = nil
	spec.FrequencyPenalty = nil
	spec.PresencePenalty = nil

	filtered := make([]provider.InvokeOption, 0, len(opts))
	for _, opt := range opts {
		switch opt.(type) {
		case provider.MaxTokensOption, *provider.MaxTokensOption:
			continue
		default:
			filtered = append(filtered, opt)
		}
	}

	return p.inner.Invoke(ctx, state, spec, ch, filtered...)
}

var _ provider.Provider = (*codexCompatibilityProvider)(nil)
