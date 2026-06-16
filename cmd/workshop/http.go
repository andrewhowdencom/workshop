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
	defaultName, providers, err := loadProvidersConfig(viper.GetViper())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	maybeStartPProf(ctx, viper.GetBool("pprof"), viper.GetString("pprof.addr"))

	tracer, tracerShutdown, err := telemetry.NewTracer(viper.GetString("telemetry.traces.endpoint"))
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerShutdown(shutdownCtx); err != nil {
			// not using slog here to avoid import cycle / nil logger in short-lived HTTP init
			fmt.Fprintf(os.Stderr, "tracer shutdown failed: %v\n", err)
		}
	}()

	meter, meterShutdown, err := telemetry.NewMeter(viper.GetString("telemetry.metrics.endpoint"))
	if err != nil {
		return fmt.Errorf("init metrics: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := meterShutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "meter shutdown failed: %v\n", err)
		}
	}()

	cwd := ""
	if d, err := os.Getwd(); err == nil {
		cwd = d
	}

	opts := []app.Option{
		app.WithDefaultProviderName(defaultName),
		app.WithStoreDir(viper.GetString("store.dir")),
		app.WithHTTPAddr(viper.GetString("http.addr")),
		app.WithWorkingDir(cwd),
		app.WithRole(viper.GetString("role")),
		app.WithTracer(tracer),
		app.WithMeter(meter),
		app.WithCompaction(app.CompactionConfig{
			Provider:  viper.GetString("compaction.provider"),
			MaxTokens: viper.GetInt("compaction.max-tokens"),
		}),
	}
	for name, pc := range providers {
		opts = append(opts, app.WithProvider(name, pc))
	}

	return app.RunHTTP(ctx, opts...)
}
