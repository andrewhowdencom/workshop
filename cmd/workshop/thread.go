package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/andrewhowdencom/ore/session"
	"github.com/andrewhowdencom/ore/x/analytics"
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

var threadAnalyticsCmd = &cobra.Command{
	Use:   "analytics [<id>]",
	Short: "Print per-artifact-kind statistics for a thread or the whole store",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runThreadAnalytics,
}

func init() {
	// thread list: paginated, sorted by recency. The default sort
	// order is implicit; no lookback filter.
	threadListCmd.Flags().Int("limit", session.DefaultPageSize,
		fmt.Sprintf("Page size (default %d, max %d, clamped)", session.DefaultPageSize, session.MaxPageSize))
	threadListCmd.Flags().String("cursor", "",
		"Opaque pagination cursor returned by a previous invocation")
	threadListCmd.Flags().Bool("all", false,
		"Walk all pages in a single call; suppress the --next hint")

	threadExportCmd.Flags().String("format", "text", "Export format (text, json, html)")
	threadExportCmd.Flags().String("output", "", "Export file path (default: stdout)")
	cobra.CheckErr(viper.BindPFlags(threadExportCmd.Flags()))

	// thread analytics: aggregated lookback. The flag stays on this
	// subcommand only; the previous shared `--days` binding on
	// thread list was removed (recency is implicit in the sort order).
	threadAnalyticsCmd.Flags().Int("days", 30, "Lookback period in days for the store-wide form")
	// NOTE: We deliberately do not call viper.BindPFlags on
	// threadAnalyticsCmd.Flags(). The previous code bound both
	// `thread list --days` and `thread analytics --days` to the same
	// viper key, and the second binding won, which silently dropped
	// the user's `--days 1` on the list command. The analytics path
	// reads the flag via cmd.Flags().GetInt below instead. The same
	// pattern applies to the list command's flags (limit, cursor, all).

	threadCmd.AddCommand(threadListCmd)
	threadCmd.AddCommand(threadExportCmd)
	threadCmd.AddCommand(threadAnalyticsCmd)
	rootCmd.AddCommand(threadCmd)
}

func runThreadList(cmd *cobra.Command, args []string) error {
	storeDir := viper.GetString("store.dir")
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}

	limit, err := cmd.Flags().GetInt("limit")
	if err != nil {
		return fmt.Errorf("read --limit: %w", err)
	}
	cursor, err := cmd.Flags().GetString("cursor")
	if err != nil {
		return fmt.Errorf("read --cursor: %w", err)
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return fmt.Errorf("read --all: %w", err)
	}

	store, err := session.NewJSONStore(storeDir)
	if err != nil {
		return fmt.Errorf("create JSON store: %w", err)
	}

	return runThreadListWithStore(limit, cursor, all, store, os.Stdout)
}

// runThreadListWithStore renders a single page of threads (or all
// pages when all is true) sorted by updated_at desc, id asc. When
// the rendered output is the first page of a multi-page result and
// all is false, a `-- next: --cursor <opaque>` hint line is emitted
// after the table so the user can continue.
//
// The function is the seam used by tests; runThreadList is the cobra
// entry point. limit is the page size (clamped by session.Paginate);
// cursor is the opaque pagination cursor from a previous call (empty
// for the first page); all walks the cursor to exhaustion and
// suppresses the hint. The store is read once into a slice; the
// helper sorts in place and returns sub-slices, so memory cost is
// O(N) full-thread reads on the first call and O(limit) per
// subsequent page in --all mode (because Paginate is re-called on
// the same underlying slice, which the caller is responsible for
// keeping populated).
func runThreadListWithStore(limit int, cursor string, all bool, store session.Store, w io.Writer) error {
	threads, err := store.List()
	if err != nil {
		return fmt.Errorf("list threads: %w", err)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\tCREATED\tUPDATED\tROLE\n")

	current := cursor
	for {
		page, next, err := session.Paginate(threads, limit, current)
		if err != nil {
			if errors.Is(err, session.ErrInvalidCursor) {
				return fmt.Errorf("invalid --cursor: %w", err)
			}
			return fmt.Errorf("paginate threads: %w", err)
		}

		for _, thr := range page {
			role := thr.Metadata["workshop.role"]
			created := thr.CreatedAt.Format("2006-01-02 15:04")
			updated := thr.UpdatedAt.Format("2006-01-02 15:04")
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", thr.ID, created, updated, role)
		}

		if all {
			if next == "" {
				break
			}
			current = next
			continue
		}

		// First-page case: render the hint line when more pages
		// remain so the user knows to invoke again with the cursor.
		// (The loop runs exactly once in this branch.)
		if next != "" {
			fmt.Fprintf(tw, "\n-- next: --cursor %s\n", next)
		}
		break
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
	thread, err := store.Get(id)
	if errors.Is(err, session.ErrThreadNotFound) {
		return fmt.Errorf("thread not found: %s", id)
	} else if err != nil {
		return fmt.Errorf("get thread: %w", err)
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

func runThreadAnalytics(cmd *cobra.Command, args []string) error {
	storeDir := viper.GetString("store.dir")
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}

	store, err := session.NewJSONStore(storeDir)
	if err != nil {
		return fmt.Errorf("create JSON store: %w", err)
	}

	id := ""
	if len(args) == 1 {
		id = args[0]
	}

	// Read --days directly from the command's flag set rather than
	// from viper. The previous implementation called viper.GetInt
	// and collided with the now-removed binding on thread list;
	// reading from cmd.Flags() makes the value the user actually
	// typed visible to this command.
	days, err := cmd.Flags().GetInt("days")
	if err != nil {
		return fmt.Errorf("read --days: %w", err)
	}

	return runThreadAnalyticsWithStore(days, id, store, os.Stdout)
}

// runThreadAnalyticsWithStore aggregates per-(kind, source)
// statistics from the given store and writes a tabwriter table to w.
//
// The output table has four columns: KIND, SOURCE, COUNT, BYTES.
// SOURCE is the originating tool name for tool_call and tool_result
// artifacts, and is empty for all other artifact kinds. This lets
// callers attribute context cost to specific tools, not just kinds.
//
// If id is non-empty, only that thread is aggregated. If id is empty,
// threads older than `days` are excluded first (matched on UpdatedAt).
//
// This function is read-only by construction: it never calls store.Save
// or store.Create, and only reads from the store via List / Get.
func runThreadAnalyticsWithStore(days int, id string, store session.Store, w io.Writer) error {
	var stats []analytics.Stats
	if id != "" {
		thread, err := store.Get(id)
		if errors.Is(err, session.ErrThreadNotFound) {
			return fmt.Errorf("thread not found: %s", id)
		} else if err != nil {
			return fmt.Errorf("get thread: %w", err)
		}
		stats = analytics.AnalyzeThread(thread)
	} else {
		cutoff := time.Now().AddDate(0, 0, -days)
		filtered := &storeFilter{Store: store, cutoff: cutoff}

		var err error
		stats, err = analytics.AnalyzeStore(filtered)
		if err != nil {
			return fmt.Errorf("analyze store: %w", err)
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "KIND\tSOURCE\tCOUNT\tBYTES\n")
	for _, s := range stats {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", s.Kind, s.Source, s.Count, s.Bytes)
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush tabwriter: %w", err)
	}
	return nil
}

// storeFilter wraps a session.Store so that List() returns only threads
// whose UpdatedAt is at-or-after the configured cutoff. It exists to let
// runThreadAnalyticsWithStore apply a --days lookback before delegating
// to analytics.AnalyzeStore, which only accepts a Store.
//
// The embedded session.Store auto-forwards all other methods unchanged;
// only List is overridden. This is intentional — the analytics path is
// read-only, but the analyzer still requires a value of type
// session.Store, so the wrapper must satisfy the full interface.
type storeFilter struct {
	session.Store
	cutoff time.Time
}

func (s *storeFilter) List() ([]*session.Thread, error) {
	threads, err := s.Store.List()
	if err != nil {
		return nil, err
	}

	filtered := make([]*session.Thread, 0, len(threads))
	for _, thr := range threads {
		if thr.UpdatedAt.After(s.cutoff) {
			filtered = append(filtered, thr)
		}
	}
	return filtered, nil
}
