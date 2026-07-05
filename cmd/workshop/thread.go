package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/andrewhowdencom/ore/junk"
	"github.com/andrewhowdencom/ore/x/analytics"
	"github.com/andrewhowdencom/ore/x/export"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Pagination parameters for `thread list`. Inlined here because the
// `ore/junk` package split `DefaultPageSize`/`MaxPageSize`/`Paginate` out
// when `session` was renamed to `junk`; the cursor format is small and
// stdlib-only, so duplicating it is simpler than depending on the
// private helper inside `x/conduit/http`.
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// errInvalidCursor is the sentinel returned by paginateThreads when the
// opaque cursor cannot be decoded. The CLI reports it as "invalid
// --cursor".
var errInvalidCursor = errors.New("invalid pagination cursor")

// threadCursor is the opaque pagination cursor. Version allows the
// encoding to evolve without breaking already-stored cursors. The cursor
// identifies the LAST item of the previous page; subsequent pages return
// items that sort strictly after this position in (last activity desc,
// id asc) order.
//
// LastAt is the timestamp of the most recent turn in the previous page's
// last thread (or the zero time for empty threads). The previous wire
// format carried UpdatedAt per-thread; that field was removed when
// ore/junk moved thread state to a tree-backed ledger (see
// ../ore/junk/thread.go and the http conduit's matching migration in
// ../ore/x/conduit/http/threads.go). The cursor field name was renamed
// in lockstep.
type threadCursor struct {
	Version int       `json:"v"`
	LastAt  time.Time `json:"l"`
	ID      string    `json:"i"`
}

const threadCursorVersion = 1

// encode returns the opaque base64-encoded JSON form of the cursor.
func (c threadCursor) encode() (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// decodeThreadCursor parses a base64-encoded JSON cursor. Returns an
// error wrapping errInvalidCursor for any parse failure, unknown
// version, or empty input.
func decodeThreadCursor(s string) (threadCursor, error) {
	if s == "" {
		return threadCursor{}, fmt.Errorf("%w: empty cursor", errInvalidCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return threadCursor{}, fmt.Errorf("%w: %v", errInvalidCursor, err)
	}
	var c threadCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return threadCursor{}, fmt.Errorf("%w: %v", errInvalidCursor, err)
	}
	if c.Version != threadCursorVersion {
		return threadCursor{}, fmt.Errorf("%w: unsupported version %d", errInvalidCursor, c.Version)
	}
	return c, nil
}

// lastActivity returns the timestamp of the most recent turn in the
// thread. Empty threads return the zero time. The returned time is the
// conversation's "last activity" — used as the sort key for the
// thread listing.
//
// Replaces the previous per-thread UpdatedAt field, which was removed
// from the wire format when ore/junk migrated to a tree-backed ledger
// (see ../ore/junk/thread.go and ../ore/x/conduit/http/threads.go).
func lastActivity(t *junk.Thread) time.Time {
	if t == nil || t.State == nil {
		return time.Time{}
	}
	turns := t.State.AllTurns()
	if len(turns) == 0 {
		return time.Time{}
	}
	return turns[len(turns)-1].Timestamp
}

// paginateThreads sorts threads by (last activity desc, id asc) and
// returns a single page of at most limit items, starting strictly after
// the position identified by cursor. An empty cursor means "start from
// the beginning". Empty threads sort last regardless of their ID.
// Returns errInvalidCursor when the cursor cannot be decoded. The input
// slice is sorted in place; the returned page is a sub-slice of the
// input.
func paginateThreads(threads []*junk.Thread, limit int, cursor string) (page []*junk.Thread, nextCursor string, err error) {
	if limit < 1 {
		limit = 1
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	slices.SortFunc(threads, compareThreads)

	start := 0
	if cursor != "" {
		c, err := decodeThreadCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		start = len(threads) // default: no items after cursor
		for i, t := range threads {
			if threadIsAfterCursor(t, c) {
				start = i
				break
			}
		}
	}

	end := start + limit
	if end > len(threads) {
		end = len(threads)
	}

	page = threads[start:end]

	if end < len(threads) {
		last := threads[end-1]
		next, encErr := (threadCursor{
			Version: threadCursorVersion,
			LastAt:  lastActivity(last),
			ID:      last.ID,
		}).encode()
		if encErr != nil {
			return nil, "", encErr
		}
		nextCursor = next
	}

	return page, nextCursor, nil
}

// compareThreads orders threads by (last activity desc, id asc). The id
// tiebreaker is required for deterministic pagination across threads
// that share a timestamp. Empty threads (zero last activity) sort last.
func compareThreads(a, b *junk.Thread) int {
	aAt := lastActivity(a)
	bAt := lastActivity(b)
	if aAt.Equal(bAt) {
		return strings.Compare(a.ID, b.ID)
	}
	if aAt.IsZero() {
		return 1 // a is empty; b first
	}
	if bAt.IsZero() {
		return -1 // b is empty; a first
	}
	if aAt.After(bAt) {
		return -1 // a comes first (later activity)
	}
	return 1
}

// threadIsAfterCursor reports whether t sorts strictly after the cursor
// position in (last activity desc, id asc) order. Items equal to the
// cursor are NOT considered "after"; the cursor is exclusive.
func threadIsAfterCursor(t *junk.Thread, c threadCursor) bool {
	tAt := lastActivity(t)
	if tAt.IsZero() {
		// Empty threads never sort after a real cursor.
		return false
	}
	if c.LastAt.IsZero() {
		// Anything with activity sorts after an empty cursor.
		return true
	}
	if tAt.Before(c.LastAt) {
		return true
	}
	if tAt.Equal(c.LastAt) && t.ID > c.ID {
		return true
	}
	return false
}

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
	threadListCmd.Flags().Int("limit", defaultPageSize,
		fmt.Sprintf("Page size (default %d, max %d, clamped)", defaultPageSize, maxPageSize))
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

	store, err := junk.NewJSONStore(storeDir)
	if err != nil {
		return fmt.Errorf("create JSON store: %w", err)
	}

	return runThreadListWithStore(limit, cursor, all, store, os.Stdout)
}

// runThreadListWithStore renders a single page of threads (or all
// pages when all is true) sorted by last activity desc, id asc. When
// the rendered output is the first page of a multi-page result and
// all is false, a `-- next: --cursor <opaque>` hint line is emitted
// after the table so the user can continue.
//
// The function is the seam used by tests; runThreadList is the cobra
// entry point. limit is the page size (clamped by paginateThreads);
// cursor is the opaque pagination cursor from a previous call (empty
// for the first page); all walks the cursor to exhaustion and
// suppresses the hint. The store is read once into a slice; the
// helper sorts in place and returns sub-slices, so memory cost is
// O(N) full-thread reads on the first call and O(limit) per
// subsequent page in --all mode (because paginateThreads is re-called
// on the same underlying slice, which the caller is responsible for
// keeping populated).
//
// The table has three columns: ID, LAST ACTIVITY, ROLE. The "LAST
// ACTIVITY" column is the timestamp of the most recent turn (see
// lastActivity). The previous CREATED column was dropped alongside
// the per-thread CreatedAt field in the same ore/junk wire-format
// change that removed UpdatedAt; the conversation's temporal data
// now lives entirely in the turn history.
func runThreadListWithStore(limit int, cursor string, all bool, store junk.Store, w io.Writer) error {
	threads, err := store.List()
	if err != nil {
		return fmt.Errorf("list threads: %w", err)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\tLAST ACTIVITY\tROLE\n")

	current := cursor
	for {
		page, next, err := paginateThreads(threads, limit, current)
		if err != nil {
			if errors.Is(err, errInvalidCursor) {
				return fmt.Errorf("invalid --cursor: %w", err)
			}
			return fmt.Errorf("paginate threads: %w", err)
		}

		for _, thr := range page {
			role := thr.Metadata["workshop.role"]
			at := lastActivity(thr)
			last := ""
			if !at.IsZero() {
				last = at.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", thr.ID, last, role)
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

	store, err := junk.NewJSONStore(storeDir)
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

func runThreadExportWithStore(store junk.Store, id, format string, w io.Writer) error {
	thread, err := store.Get(id)
	if errors.Is(err, junk.ErrThreadNotFound) {
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

	store, err := junk.NewJSONStore(storeDir)
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
// threads with no turn activity since `days` ago are excluded first
// (matched against the thread's last-activity timestamp; see
// lastActivity).
//
// This function is read-only by construction: it never calls store.Save
// or store.Create, and only reads from the store via List / Get.
func runThreadAnalyticsWithStore(days int, id string, store junk.Store, w io.Writer) error {
	var stats []analytics.Stats
	if id != "" {
		thread, err := store.Get(id)
		if errors.Is(err, junk.ErrThreadNotFound) {
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

// storeFilter wraps a junk.Store so that List() returns only threads
// whose last-activity timestamp is at-or-after the configured cutoff.
// It exists to let runThreadAnalyticsWithStore apply a --days lookback
// before delegating to analytics.AnalyzeStore, which only accepts a
// Store.
//
// The embedded junk.Store auto-forwards all other methods unchanged;
// only List is overridden. This is intentional — the analytics path is
// read-only, but the analyzer still requires a value of type
// junk.Store, so the wrapper must satisfy the full interface.
type storeFilter struct {
	junk.Store
	cutoff time.Time
}

func (s *storeFilter) List() ([]*junk.Thread, error) {
	threads, err := s.Store.List()
	if err != nil {
		return nil, err
	}

	filtered := make([]*junk.Thread, 0, len(threads))
	for _, thr := range threads {
		at := lastActivity(thr)
		// Empty threads (no turns) and threads with last activity
		// before the cutoff are both excluded. A thread whose last
		// activity equals the cutoff exactly is included so the
		// boundary is consistent with the >= comparison used
		// elsewhere in the analytics pipeline.
		if at.IsZero() || at.Before(s.cutoff) {
			continue
		}
		filtered = append(filtered, thr)
	}
	return filtered, nil
}
