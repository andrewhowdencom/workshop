package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/andrewhowdencom/workshop/internal/app"
	"github.com/andrewhowdencom/workshop/internal/telemetry"
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
	pc := makeProviderConfig()
	if pc.APIKey == "" {
		return fmt.Errorf("api key is required; set --provider.api-key or WORKSHOP_PROVIDER_API_KEY environment variable")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	maybeStartPProf(ctx, viper.GetBool("pprof"), viper.GetString("pprof.addr"))

	tracer, shutdown, err := telemetry.NewTracer(viper.GetString("tracing.endpoint"))
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			// not using slog here to avoid import cycle / nil logger in short-lived HTTP init
			fmt.Fprintf(os.Stderr, "tracer shutdown failed: %v\n", err)
		}
	}()

	cwd := ""
	if d, err := os.Getwd(); err == nil {
		cwd = d
	}

	return app.RunHTTP(ctx,
		app.WithProvider(pc),
		app.WithStoreDir(viper.GetString("store.dir")),
		app.WithHTTPAddr(viper.GetString("http.addr")),
		app.WithWorkingDir(cwd),
		app.WithRole(viper.GetString("role")),
		app.WithTracer(tracer),
		app.WithCompaction(app.CompactionConfig{
			MaxTokens:     viper.GetInt("compaction.max-tokens"),
			PreserveLastN: viper.GetInt("compaction.preserve-last-n"),
		}),
	)
}
