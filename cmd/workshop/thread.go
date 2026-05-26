package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/andrewhowdencom/ore/thread"
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

func init() {
	threadListCmd.Flags().Int("days", 30, "Lookback period in days")
	cobra.CheckErr(viper.BindPFlags(threadListCmd.Flags()))

	threadCmd.AddCommand(threadListCmd)
	rootCmd.AddCommand(threadCmd)
}

func runThreadList(cmd *cobra.Command, args []string) error {
	storeDir := viper.GetString("store.dir")
	if storeDir == "" {
		fmt.Fprintln(os.Stderr, "warning: store.dir is not set; using in-memory store (no persistent threads to list)")
		return fmt.Errorf("store.dir must be set for thread list")
	}

	days := viper.GetInt("days")

	store, err := thread.NewJSONStore(storeDir)
	if err != nil {
		return fmt.Errorf("create JSON store: %w", err)
	}

	return runThreadListWithStore(days, store, os.Stdout)
}

func runThreadListWithStore(days int, store thread.Store, w io.Writer) error {
	threads, err := store.List()
	if err != nil {
		return fmt.Errorf("list threads: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -days)

	var filtered []*thread.Thread
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
		role := ""
		if r, ok := thr.GetMetadata("workshop.role"); ok {
			role = r
		}
		created := thr.CreatedAt.Format("2006-01-02 15:04")
		updated := thr.UpdatedAt.Format("2006-01-02 15:04")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", thr.ID, created, updated, role)
	}

	return tw.Flush()
}
