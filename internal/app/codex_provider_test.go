package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrewhowdencom/ore/artifact"
	"github.com/andrewhowdencom/ore/ledger"
	"github.com/andrewhowdencom/ore/models"
	"github.com/andrewhowdencom/ore/provider"
	"github.com/andrewhowdencom/ore/x/provider/codex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexCompatibilityProviderOmitsUnsupportedRequestFields(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	credentialPath := filepath.Join(t.TempDir(), "credentials.json")
	require.NoError(t, os.WriteFile(credentialPath, []byte(`{
		"access_token": "test-token",
		"account_id": "test-account"
	}`), 0o600))

	inner, err := codex.New(
		codex.WithHTTPClient(server.Client()),
		codex.WithResponsesEndpoint(server.URL),
		codex.WithCredentialPath(credentialPath),
	)
	require.NoError(t, err)
	p := &codexCompatibilityProvider{inner: inner}

	temperature, topP, frequencyPenalty, presencePenalty := 0.7, 0.8, 0.2, 0.3
	topK, seed := 40, int64(42)
	err = p.Invoke(context.Background(), ledger.NewThread(), models.Spec{
		Name:             "gpt-test",
		MaxOutputTokens:  100_000,
		Temperature:      &temperature,
		TopP:             &topP,
		TopK:             &topK,
		Seed:             &seed,
		StopSequences:    []string{"stop"},
		FrequencyPenalty: &frequencyPenalty,
		PresencePenalty:  &presencePenalty,
	}, make(chan artifact.Artifact, 4), provider.WithMaxTokens(50_000))
	require.NoError(t, err)

	assert.Equal(t, "gpt-test", request["model"])
	assert.Equal(t, true, request["stream"])
	assert.Equal(t, false, request["store"])
	for _, field := range []string{
		"max_output_tokens",
		"temperature",
		"top_p",
		"top_k",
		"seed",
		"stop",
		"frequency_penalty",
		"presence_penalty",
	} {
		assert.NotContains(t, request, field)
	}
}
