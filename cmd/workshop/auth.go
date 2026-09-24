package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/andrewhowdencom/ore/x/provider/codex"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// authBackend is the authentication behavior of the currently selected
// provider. Adding another interactive authentication scheme only requires a
// new implementation and a case in configuredAuthBackend.
type authBackend interface {
	Login(context.Context, io.Writer) error
	Status(io.Writer) error
	Logout(context.Context, io.Writer) error
}

// authClient keeps the command layer independent from OAuth implementation
// details while preserving ore/codex as the owner of credentials and refresh.
type authClient interface {
	LoggedIn() bool
	StartDeviceLogin(context.Context) (*deviceLogin, error)
	Logout(context.Context) error
}

type deviceLogin struct {
	verificationURL string
	userCode        string
	wait            func(context.Context) error
	cancel          func()
}

type oreCodexAuthClient struct {
	provider *codex.Provider
}

func (c *oreCodexAuthClient) LoggedIn() bool { return c.provider.LoggedIn() }

func (c *oreCodexAuthClient) StartDeviceLogin(ctx context.Context) (*deviceLogin, error) {
	login, err := c.provider.StartDeviceLogin(ctx)
	if err != nil {
		return nil, err
	}
	return &deviceLogin{
		verificationURL: login.VerificationURL,
		userCode:        login.UserCode,
		wait:            login.Wait,
		cancel:          login.Cancel,
	}, nil
}

func (c *oreCodexAuthClient) Logout(ctx context.Context) error {
	return c.provider.Logout(ctx)
}

var newAuthClient = func() (authClient, error) {
	provider, err := codex.New(codex.WithOriginator("workshop"))
	if err != nil {
		return nil, err
	}
	return &oreCodexAuthClient{provider: provider}, nil
}

type codexAuthBackend struct {
	name   string
	client authClient
}

func (b *codexAuthBackend) Login(ctx context.Context, out io.Writer) error {
	login, err := b.client.StartDeviceLogin(ctx)
	if err != nil {
		return fmt.Errorf("start device login: %w", err)
	}
	defer login.cancel()

	if _, err := fmt.Fprintf(out, "Provider %q: open %s and enter code %s\n", b.name, login.verificationURL, login.userCode); err != nil {
		return fmt.Errorf("write login instructions: %w", err)
	}
	if err := login.wait(ctx); err != nil {
		return fmt.Errorf("complete device login: %w", err)
	}
	if _, err := fmt.Fprintf(out, "Provider %q login complete.\n", b.name); err != nil {
		return fmt.Errorf("write login result: %w", err)
	}
	return nil
}

func (b *codexAuthBackend) Status(out io.Writer) error {
	if b.client.LoggedIn() {
		_, err := fmt.Fprintf(out, "Provider %q: Codex credentials are present.\n", b.name)
		return err
	} else {
		_, err := fmt.Fprintf(out, "Provider %q: Codex credentials are not present. Run `workshop auth login`.\n", b.name)
		return err
	}
}

func (b *codexAuthBackend) Logout(ctx context.Context, out io.Writer) error {
	if err := b.client.Logout(ctx); err != nil {
		return fmt.Errorf("revoke Codex credentials: %w", err)
	}
	_, err := fmt.Fprintf(out, "Provider %q logged out.\n", b.name)
	return err
}

type apiKeyAuthBackend struct {
	name       string
	kind       string
	configured bool
}

func (b *apiKeyAuthBackend) Login(context.Context, io.Writer) error {
	return fmt.Errorf(
		"provider %q (%s) uses API-key authentication; configure providers.%s.api-key or its environment override",
		b.name, b.kind, b.name,
	)
}

func (b *apiKeyAuthBackend) Status(out io.Writer) error {
	state := "not configured"
	if b.configured {
		state = "configured"
	}
	_, err := fmt.Fprintf(out, "Provider %q: %s API key is %s.\n", b.name, b.kind, state)
	return err
}

func (b *apiKeyAuthBackend) Logout(context.Context, io.Writer) error {
	return fmt.Errorf(
		"provider %q (%s) uses config-managed API-key authentication; remove providers.%s.api-key or its environment override to log out",
		b.name, b.kind, b.name,
	)
}

func configuredAuthBackend(v *viper.Viper) (authBackend, error) {
	name, providers, err := loadProvidersConfig(v)
	if err != nil {
		return nil, err
	}
	providerConfig := providers[name]
	kind := providerConfig.Kind
	if kind == "" {
		kind = "openai"
	}

	switch kind {
	case "codex":
		client, err := newAuthClient()
		if err != nil {
			return nil, fmt.Errorf("initialize provider %q authentication: %w", name, err)
		}
		return &codexAuthBackend{name: name, client: client}, nil
	case "openai", "anthropic":
		return &apiKeyAuthBackend{name: name, kind: kind, configured: providerConfig.APIKey != ""}, nil
	default:
		return nil, fmt.Errorf("provider %q has unsupported authentication kind %q", name, kind)
	}
}

var newCurrentAuthBackend = func() (authBackend, error) {
	return configuredAuthBackend(viper.GetViper())
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication for the selected provider",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate the selected provider",
	Long: `Authenticate the currently selected provider.

For Codex, the command prints a verification URL and one-time code, then waits
for browser authorization. API-key providers report the configuration field
that supplies their credentials.`,
	RunE: runAuthLogin,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status for the selected provider",
	RunE:  runAuthStatus,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove authentication for the selected provider",
	RunE:  runAuthLogout,
}

func init() {
	authCmd.AddCommand(authLoginCmd, authStatusCmd, authLogoutCmd)
	rootCmd.AddCommand(authCmd)
}

func runAuthLogin(cmd *cobra.Command, _ []string) error {
	backend, err := newCurrentAuthBackend()
	if err != nil {
		return fmt.Errorf("resolve authentication: %w", err)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	if err := backend.Login(ctx, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("authenticate selected provider: %w", err)
	}
	return nil
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	backend, err := newCurrentAuthBackend()
	if err != nil {
		return fmt.Errorf("resolve authentication: %w", err)
	}
	if err := backend.Status(cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("read authentication status: %w", err)
	}
	return nil
}

func runAuthLogout(cmd *cobra.Command, _ []string) error {
	backend, err := newCurrentAuthBackend()
	if err != nil {
		return fmt.Errorf("resolve authentication: %w", err)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	if err := backend.Logout(ctx, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("log out selected provider: %w", err)
	}
	return nil
}
