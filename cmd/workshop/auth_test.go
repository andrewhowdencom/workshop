package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type fakeAuthClient struct {
	loggedIn  bool
	startErr  error
	waitErr   error
	logoutErr error
	started   bool
	waited    bool
	cancelled bool
	loggedOut bool
}

func (f *fakeAuthClient) LoggedIn() bool { return f.loggedIn }

func (f *fakeAuthClient) StartDeviceLogin(context.Context) (*deviceLogin, error) {
	f.started = true
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &deviceLogin{
		verificationURL: "https://example.test/device",
		userCode:        "ABCD-EFGH",
		wait: func(context.Context) error {
			f.waited = true
			return f.waitErr
		},
		cancel: func() { f.cancelled = true },
	}, nil
}

func (f *fakeAuthClient) Logout(context.Context) error {
	f.loggedOut = true
	return f.logoutErr
}

func commandWithOutput() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	return cmd, &output
}

func useAuthBackend(t *testing.T, backend authBackend) {
	t.Helper()
	original := newCurrentAuthBackend
	newCurrentAuthBackend = func() (authBackend, error) { return backend, nil }
	t.Cleanup(func() { newCurrentAuthBackend = original })
}

func TestRunAuthLogin(t *testing.T) {
	client := &fakeAuthClient{}
	useAuthBackend(t, &codexAuthBackend{name: "codex", client: client})
	cmd, output := commandWithOutput()

	if err := runAuthLogin(cmd, nil); err != nil {
		t.Fatalf("runAuthLogin: %v", err)
	}
	if !client.started || !client.waited || !client.cancelled {
		t.Fatalf("login lifecycle = started:%v waited:%v cancelled:%v", client.started, client.waited, client.cancelled)
	}
	for _, want := range []string{`Provider "codex"`, "https://example.test/device", "ABCD-EFGH", "login complete"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRunAuthLoginWaitError(t *testing.T) {
	client := &fakeAuthClient{waitErr: context.Canceled}
	useAuthBackend(t, &codexAuthBackend{name: "codex", client: client})
	cmd, _ := commandWithOutput()

	err := runAuthLogin(cmd, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runAuthLogin error = %v, want context.Canceled", err)
	}
}

func TestRunAuthStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		loggedIn bool
		want     string
	}{
		{name: "credentials present", loggedIn: true, want: `Provider "codex": Codex credentials are present.`},
		{name: "logged out", want: "workshop auth login"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeAuthClient{loggedIn: test.loggedIn}
			useAuthBackend(t, &codexAuthBackend{name: "codex", client: client})
			cmd, output := commandWithOutput()
			if err := runAuthStatus(cmd, nil); err != nil {
				t.Fatalf("runAuthStatus: %v", err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output %q does not contain %q", output.String(), test.want)
			}
		})
	}
}

func TestRunAuthLogout(t *testing.T) {
	client := &fakeAuthClient{}
	useAuthBackend(t, &codexAuthBackend{name: "codex", client: client})
	cmd, output := commandWithOutput()

	if err := runAuthLogout(cmd, nil); err != nil {
		t.Fatalf("runAuthLogout: %v", err)
	}
	if !client.loggedOut {
		t.Fatal("Logout was not called")
	}
	if !strings.Contains(output.String(), `Provider "codex" logged out.`) {
		t.Fatalf("unexpected output: %q", output.String())
	}
}

func TestAPIKeyAuthBackend(t *testing.T) {
	backend := &apiKeyAuthBackend{name: "sonnet", kind: "anthropic", configured: true}
	useAuthBackend(t, backend)

	t.Run("status", func(t *testing.T) {
		cmd, output := commandWithOutput()
		if err := runAuthStatus(cmd, nil); err != nil {
			t.Fatalf("runAuthStatus: %v", err)
		}
		if !strings.Contains(output.String(), `Provider "sonnet": anthropic API key is configured.`) {
			t.Fatalf("unexpected output: %q", output.String())
		}
	})

	t.Run("login directs configuration", func(t *testing.T) {
		cmd, _ := commandWithOutput()
		err := runAuthLogin(cmd, nil)
		if err == nil || !strings.Contains(err.Error(), "providers.sonnet.api-key") {
			t.Fatalf("runAuthLogin error = %v, want API-key configuration guidance", err)
		}
	})

	t.Run("logout directs configuration", func(t *testing.T) {
		cmd, _ := commandWithOutput()
		err := runAuthLogout(cmd, nil)
		if err == nil || !strings.Contains(err.Error(), "providers.sonnet.api-key") {
			t.Fatalf("runAuthLogout error = %v, want API-key removal guidance", err)
		}
	})
}

func TestConfiguredAuthBackendUsesSelectedProvider(t *testing.T) {
	t.Run("API key", func(t *testing.T) {
		v := newTestViper()
		v.Set("provider", "openai-fast")
		v.Set("providers.openai-fast.kind", "openai")
		v.Set("providers.openai-fast.model", "gpt-4o")
		v.Set("providers.openai-fast.api-key", "sk-test")

		backend, err := configuredAuthBackend(v)
		if err != nil {
			t.Fatalf("configuredAuthBackend: %v", err)
		}
		got, ok := backend.(*apiKeyAuthBackend)
		if !ok {
			t.Fatalf("backend = %T, want *apiKeyAuthBackend", backend)
		}
		if got.name != "openai-fast" || got.kind != "openai" || !got.configured {
			t.Fatalf("backend = %+v", got)
		}
	})

	t.Run("Codex", func(t *testing.T) {
		original := newAuthClient
		newAuthClient = func() (authClient, error) { return &fakeAuthClient{}, nil }
		t.Cleanup(func() { newAuthClient = original })

		v := newTestViper()
		v.Set("provider", "subscription")
		v.Set("providers.subscription.kind", "codex")
		v.Set("providers.subscription.model", "gpt-5.3-codex")

		backend, err := configuredAuthBackend(v)
		if err != nil {
			t.Fatalf("configuredAuthBackend: %v", err)
		}
		got, ok := backend.(*codexAuthBackend)
		if !ok {
			t.Fatalf("backend = %T, want *codexAuthBackend", backend)
		}
		if got.name != "subscription" {
			t.Fatalf("backend name = %q, want subscription", got.name)
		}
	})
}
