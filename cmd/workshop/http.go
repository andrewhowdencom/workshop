package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/andrewhowdencom/workshop/internal/app"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	httpCmd.Flags().String("http.addr", ":8080", "TCP address for the HTTP server (e.g. :8080)")
	cobra.CheckErr(viper.BindPFlags(httpCmd.Flags()))
	rootCmd.AddCommand(httpCmd)
}

var httpCmd = &cobra.Command{
	Use:   "http",
	Short: "Run the web UI HTTP server",
	Long: `Start the HTTP web UI server for the workshop coding assistant.

The server exposes a stateful chat API with NDJSON streaming, SSE events,
and an embedded web chat client at the root path.

Usage:

	workshop http
	workshop http --http.addr :7654`,
	PersistentPreRunE: configureLogging,
	RunE:              runHTTP,
}

func runHTTP(cmd *cobra.Command, args []string) error {
	apiKey := viper.GetString("provider.api-key")
	if apiKey == "" {
		return fmt.Errorf("api key is required; set --provider.api-key or WORKSHOP_PROVIDER_API_KEY environment variable")
	}

	pc := app.ProviderConfig{
		Kind:            viper.GetString("provider.kind"),
		APIKey:          apiKey,
		Model:           viper.GetString("provider.model"),
		BaseURL:         viper.GetString("provider.base-url"),
		Temperature:     viper.GetFloat64("provider.temperature"),
		ReasoningEffort: viper.GetString("provider.reasoning-effort"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cwd := ""
	if d, err := os.Getwd(); err == nil {
		cwd = d
	}

	return app.RunHTTP(ctx,
		app.WithProvider(pc),
		app.WithStoreDir(viper.GetString("store.dir")),
		app.WithHTTPAddr(viper.GetString("http.addr")),
		app.WithWorkingDir(cwd),
	)
}
