package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/x/export"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var threadCmd = &cobra.Command{
	Use:   "thread",
	Short: "Manage persistent threads",
}

var threadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List persistent threads",
	RunE:  runThreadList,
}

var threadExportCmd = &cobra.Command{
	Use:   "export <id>",
	Short: "Export a single thread",
	Args:  cobra.ExactArgs(1),
	RunE:  runThreadExport,
}

func init() {
	threadListCmd.Flags().Int("days", 30, "Lookback period in days")
	cobra.CheckErr(viper.BindPFlags(threadListCmd.Flags()))

	threadExportCmd.Flags().String("format", "text", "Export format (text, json, html)")
	threadExportCmd.Flags().String("output", "", "Output file path (default: stdout)")
	cobra.CheckErr(viper.BindPFlags(threadExportCmd.Flags()))

	threadCmd.AddCommand(threadListCmd)
	threadCmd.AddCommand(threadExportCmd)
	rootCmd.AddCommand(threadCmd)
}

func runThreadList(cmd *cobra.Command, args []string) error {
	storeDir := viper.GetString("store.dir")
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}

	days := viper.GetInt("days")

	store, err := session.NewJSONStore(storeDir)
	if err != nil {
		return fmt.Errorf("create JSON store: %w", err)
	}

	return runThreadListWithStore(days, store, os.Stdout)
}

func runThreadListWithStore(days int, store session.Store, w io.Writer) error {
	threads, err := store.List()
	if err != nil {
		return fmt.Errorf("list threads: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -days)

	var filtered []*session.Thread
	for _, thr := range threads {
		if thr.UpdatedAt.After(cutoff) {
			filtered = append(filtered, thr)
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\tCREATED\tUPDATED\tROLE\n")
	for _, thr := range filtered {
		role := thr.Metadata["workshop.role"]
		created := thr.CreatedAt.Format("2006-01-02 15:04")
		updated := thr.UpdatedAt.Format("2006-01-02 15:04")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", thr.ID, created, updated, role)
	}

	return tw.Flush()
}

func runThreadExport(cmd *cobra.Command, args []string) error {
	storeDir := viper.GetString("store.dir")
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}

	store, err := session.NewJSONStore(storeDir)
	if err != nil {
		return fmt.Errorf("create JSON store: %w", err)
	}

	format := viper.GetString("format")
	output := viper.GetString("output")

	var w io.Writer = os.Stdout
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	return runThreadExportWithStore(store, args[0], format, w)
}

func runThreadExportWithStore(store session.Store, id, format string, w io.Writer) error {
	thread, ok := store.Get(id)
	if !ok {
		return fmt.Errorf("thread not found: %s", id)
	}

	switch format {
	case "text":
		return export.Text(w, thread)
	case "json":
		return export.JSON(w, thread)
	case "html":
		return export.HTML(w, thread)
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}
