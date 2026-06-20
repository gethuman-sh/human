package cmdamplitude

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/knowledge/amplitude"
)

// --- Interfaces ---

// amplitudeEventLister lists event types.
type amplitudeEventLister interface {
	ListEvents(ctx context.Context) ([]amplitude.EventType, error)
}

// amplitudeSegmentationQuerier runs segmentation queries.
type amplitudeSegmentationQuerier interface {
	QuerySegmentation(ctx context.Context, eventType, start, end, metric, interval string) (*amplitude.SegmentationResult, error)
}

// amplitudeTaxonomyEventLister lists taxonomy events.
type amplitudeTaxonomyEventLister interface {
	ListTaxonomyEvents(ctx context.Context) ([]amplitude.TaxonomyEvent, error)
}

// amplitudeTaxonomyUserPropLister lists taxonomy user properties.
type amplitudeTaxonomyUserPropLister interface {
	ListTaxonomyUserProperties(ctx context.Context) ([]amplitude.TaxonomyUserProperty, error)
}

// amplitudeFunnelQuerier runs funnel queries.
type amplitudeFunnelQuerier interface {
	QueryFunnel(ctx context.Context, events []string, start, end string) (*amplitude.FunnelResult, error)
}

// amplitudeRetentionQuerier runs retention queries.
type amplitudeRetentionQuerier interface {
	QueryRetention(ctx context.Context, startEvent, returnEvent, start, end string) (*amplitude.RetentionResult, error)
}

// amplitudeUserSearcher searches for users.
type amplitudeUserSearcher interface {
	SearchUsers(ctx context.Context, query string) ([]amplitude.UserMatch, error)
}

// amplitudeUserActivityGetter gets user activity.
type amplitudeUserActivityGetter interface {
	GetUserActivity(ctx context.Context, amplitudeID string) (*amplitude.UserActivity, error)
}

// amplitudeCohortLister lists cohorts.
type amplitudeCohortLister interface {
	ListCohorts(ctx context.Context) ([]amplitude.Cohort, error)
}

// --- Command builders ---

// BuildAmplitudeCommands returns the top-level "amplitude" command tree.
func BuildAmplitudeCommands() *cobra.Command {
	ampCmd := &cobra.Command{
		Use:   "amplitude",
		Short: "Amplitude product analytics",
	}

	ampCmd.PersistentFlags().String("amplitude", "", "Named Amplitude instance from .humanconfig")

	ampCmd.AddCommand(buildAmplitudeEventsCommands())
	ampCmd.AddCommand(buildAmplitudeTaxonomyCommands())
	ampCmd.AddCommand(buildAmplitudeFunnelCmd())
	ampCmd.AddCommand(buildAmplitudeRetentionCmd())
	ampCmd.AddCommand(buildAmplitudeUserCommands())
	ampCmd.AddCommand(buildAmplitudeCohortsCommands())

	return ampCmd
}

func buildAmplitudeEventsCommands() *cobra.Command {
	eventsCmd := &cobra.Command{
		Use:   "events",
		Short: "Event analytics",
	}

	eventsCmd.AddCommand(buildAmplitudeEventsListCmd())
	eventsCmd.AddCommand(buildAmplitudeEventsQueryCmd())

	return eventsCmd
}

func buildAmplitudeEventsListCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List event types with active user counts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeEventsList(cmd.Context(), client, cmd.OutOrStdout(), table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeEventsQueryCmd() *cobra.Command {
	var (
		table    bool
		event    string
		start    string
		end      string
		metric   string
		interval string
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query event segmentation data",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeEventsQuery(cmd.Context(), client, cmd.OutOrStdout(), event, start, end, metric, interval, table)
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "Event type to query")
	_ = cmd.MarkFlagRequired("event")
	cmd.Flags().StringVar(&start, "start", "", "Start date (YYYYMMDD)")
	_ = cmd.MarkFlagRequired("start")
	cmd.Flags().StringVar(&end, "end", "", "End date (YYYYMMDD)")
	_ = cmd.MarkFlagRequired("end")
	cmd.Flags().StringVar(&metric, "metric", "uniques", "Metric (uniques, totals, avg)")
	cmd.Flags().StringVar(&interval, "interval", "1", "Interval (1=daily, 7=weekly, 30=monthly)")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeTaxonomyCommands() *cobra.Command {
	taxCmd := &cobra.Command{
		Use:   "taxonomy",
		Short: "Event and property schemas",
	}

	taxCmd.AddCommand(buildAmplitudeTaxonomyEventsCmd())
	taxCmd.AddCommand(buildAmplitudeTaxonomyUserPropsCmd())

	return taxCmd
}

func buildAmplitudeTaxonomyEventsCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List event type schema",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeTaxonomyEvents(cmd.Context(), client, cmd.OutOrStdout(), table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeTaxonomyUserPropsCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "user-properties",
		Short: "List user property schema",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeTaxonomyUserProps(cmd.Context(), client, cmd.OutOrStdout(), table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeFunnelCmd() *cobra.Command {
	var (
		table  bool
		events string
		start  string
		end    string
	)
	cmd := &cobra.Command{
		Use:   "funnel",
		Short: "Funnel conversion analysis",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			eventList := cmdutil.SplitIDs(events)
			return runAmplitudeFunnel(cmd.Context(), client, cmd.OutOrStdout(), eventList, start, end, table)
		},
	}
	cmd.Flags().StringVar(&events, "events", "", "Comma-separated event types (e.g. signup,purchase)")
	_ = cmd.MarkFlagRequired("events")
	cmd.Flags().StringVar(&start, "start", "", "Start date (YYYYMMDD)")
	_ = cmd.MarkFlagRequired("start")
	cmd.Flags().StringVar(&end, "end", "", "End date (YYYYMMDD)")
	_ = cmd.MarkFlagRequired("end")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeRetentionCmd() *cobra.Command {
	var (
		table       bool
		startEvent  string
		returnEvent string
		start       string
		end         string
	)
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Retention curve analysis",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeRetention(cmd.Context(), client, cmd.OutOrStdout(), startEvent, returnEvent, start, end, table)
		},
	}
	cmd.Flags().StringVar(&startEvent, "start-event", "", "Start event type")
	_ = cmd.MarkFlagRequired("start-event")
	cmd.Flags().StringVar(&returnEvent, "return-event", "", "Return event type")
	_ = cmd.MarkFlagRequired("return-event")
	cmd.Flags().StringVar(&start, "start", "", "Start date (YYYYMMDD)")
	_ = cmd.MarkFlagRequired("start")
	cmd.Flags().StringVar(&end, "end", "", "End date (YYYYMMDD)")
	_ = cmd.MarkFlagRequired("end")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeUserCommands() *cobra.Command {
	userCmd := &cobra.Command{
		Use:   "user",
		Short: "User lookup and activity",
	}

	userCmd.AddCommand(buildAmplitudeUserSearchCmd())
	userCmd.AddCommand(buildAmplitudeUserActivityCmd())

	return userCmd
}

func buildAmplitudeUserSearchCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search for users",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeUserSearch(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeUserActivityCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "activity AMPLITUDE_ID",
		Short: "Get user event history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeUserActivity(cmd.Context(), client, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildAmplitudeCohortsCommands() *cobra.Command {
	cohortsCmd := &cobra.Command{
		Use:   "cohorts",
		Short: "Behavioral cohorts",
	}

	cohortsCmd.AddCommand(buildAmplitudeCohortsListCmd())

	return cohortsCmd
}

func buildAmplitudeCohortsListCmd() *cobra.Command {
	var table bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List behavioral cohorts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveAmplitudeClient(cmd)
			if err != nil {
				return err
			}
			return runAmplitudeCohortsList(cmd.Context(), client, cmd.OutOrStdout(), table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// --- Client resolution ---

func resolveAmplitudeClient(cmd *cobra.Command) (*amplitude.Client, error) {
	name, _ := cmd.Flags().GetString("amplitude")

	instances, err := amplitude.LoadInstances(".")
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, errors.WithDetails("no Amplitude instances configured, add amplitudes: to .humanconfig.yaml")
	}

	if name != "" {
		for _, inst := range instances {
			if inst.Name == name {
				return inst.Client, nil
			}
		}
		return nil, errors.WithDetails("Amplitude instance not found", "name", name)
	}

	return instances[0].Client, nil
}

// --- Business logic functions ---

func runAmplitudeEventsList(ctx context.Context, client amplitudeEventLister, out io.Writer, table bool) error {
	events, err := client.ListEvents(ctx)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeEventsTable(out, events)
	}
	return cmdutil.PrintJSON(out, events)
}

func runAmplitudeEventsQuery(ctx context.Context, client amplitudeSegmentationQuerier, out io.Writer, event, start, end, metric, interval string, table bool) error {
	result, err := client.QuerySegmentation(ctx, event, start, end, metric, interval)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeSegmentationTable(out, result)
	}
	return cmdutil.PrintJSON(out, result)
}

func runAmplitudeTaxonomyEvents(ctx context.Context, client amplitudeTaxonomyEventLister, out io.Writer, table bool) error {
	events, err := client.ListTaxonomyEvents(ctx)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeTaxonomyEventsTable(out, events)
	}
	return cmdutil.PrintJSON(out, events)
}

func runAmplitudeTaxonomyUserProps(ctx context.Context, client amplitudeTaxonomyUserPropLister, out io.Writer, table bool) error {
	props, err := client.ListTaxonomyUserProperties(ctx)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeTaxonomyUserPropsTable(out, props)
	}
	return cmdutil.PrintJSON(out, props)
}

func runAmplitudeFunnel(ctx context.Context, client amplitudeFunnelQuerier, out io.Writer, events []string, start, end string, table bool) error {
	result, err := client.QueryFunnel(ctx, events, start, end)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeFunnelTable(out, result)
	}
	return cmdutil.PrintJSON(out, result)
}

func runAmplitudeRetention(ctx context.Context, client amplitudeRetentionQuerier, out io.Writer, startEvent, returnEvent, start, end string, table bool) error {
	result, err := client.QueryRetention(ctx, startEvent, returnEvent, start, end)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeRetentionTable(out, result)
	}
	return cmdutil.PrintJSON(out, result)
}

func runAmplitudeUserSearch(ctx context.Context, client amplitudeUserSearcher, out io.Writer, query string, table bool) error {
	matches, err := client.SearchUsers(ctx, query)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeUserSearchTable(out, matches)
	}
	return cmdutil.PrintJSON(out, matches)
}

func runAmplitudeUserActivity(ctx context.Context, client amplitudeUserActivityGetter, out io.Writer, amplitudeID string, table bool) error {
	activity, err := client.GetUserActivity(ctx, amplitudeID)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeUserActivityTable(out, activity)
	}
	return cmdutil.PrintJSON(out, activity)
}

func runAmplitudeCohortsList(ctx context.Context, client amplitudeCohortLister, out io.Writer, table bool) error {
	cohorts, err := client.ListCohorts(ctx)
	if err != nil {
		return err
	}
	if table {
		return printAmplitudeCohortsTable(out, cohorts)
	}
	return cmdutil.PrintJSON(out, cohorts)
}

// --- Output formatters ---

func printAmplitudeEventsTable(out io.Writer, events []amplitude.EventType) error {
	if len(events) == 0 {
		_, _ = fmt.Fprintln(out, "No events found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tTOTAL USERS")
	for _, e := range events {
		_, _ = fmt.Fprintf(w, "%s\t%d\n", e.Name, e.TotalUsers)
	}
	return w.Flush()
}

func printAmplitudeSegmentationTable(out io.Writer, r *amplitude.SegmentationResult) error {
	_, _ = fmt.Fprintf(out, "Event: %s\n\n", r.EventType)
	if len(r.Dates) == 0 {
		_, _ = fmt.Fprintln(out, "No data")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "DATE\tVALUE")
	for i, d := range r.Dates {
		val := 0.0
		if i < len(r.Values) {
			val = r.Values[i]
		}
		_, _ = fmt.Fprintf(w, "%s\t%.0f\n", d, val)
	}
	return w.Flush()
}

func printAmplitudeTaxonomyEventsTable(out io.Writer, events []amplitude.TaxonomyEvent) error {
	if len(events) == 0 {
		_, _ = fmt.Fprintln(out, "No taxonomy events found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tCATEGORY\tDESCRIPTION")
	for _, e := range events {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, e.Category, e.Description)
	}
	return w.Flush()
}

func printAmplitudeTaxonomyUserPropsTable(out io.Writer, props []amplitude.TaxonomyUserProperty) error {
	if len(props) == 0 {
		_, _ = fmt.Fprintln(out, "No user properties found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tTYPE\tDESCRIPTION")
	for _, p := range props {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, p.Type, p.Description)
	}
	return w.Flush()
}

func printAmplitudeFunnelTable(out io.Writer, r *amplitude.FunnelResult) error {
	if len(r.Steps) == 0 {
		_, _ = fmt.Fprintln(out, "No funnel data")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "STEP\tEVENT\tCOUNT\tCONVERSION %")
	for i, s := range r.Steps {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%d\t%.1f%%\n", i+1, s.Name, s.Count, s.ConversionPct*100)
	}
	return w.Flush()
}

func printAmplitudeRetentionTable(out io.Writer, r *amplitude.RetentionResult) error {
	_, _ = fmt.Fprintf(out, "Start: %s  Return: %s\n\n", r.StartEvent, r.ReturnEvent)
	if len(r.Days) == 0 {
		_, _ = fmt.Fprintln(out, "No retention data")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "DAY\tCOUNT\tRETENTION %")
	for _, d := range r.Days {
		_, _ = fmt.Fprintf(w, "%d\t%d\t%.1f%%\n", d.Day, d.Count, d.Pct*100)
	}
	return w.Flush()
}

func printAmplitudeUserSearchTable(out io.Writer, matches []amplitude.UserMatch) error {
	if len(matches) == 0 {
		_, _ = fmt.Fprintln(out, "No users found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "AMPLITUDE ID\tUSER ID\tPLATFORM\tCOUNTRY\tLAST SEEN")
	for _, m := range matches {
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", m.AmplitudeID, m.UserID, m.Platform, m.Country, m.LastSeen)
	}
	return w.Flush()
}

func printAmplitudeUserActivityTable(out io.Writer, a *amplitude.UserActivity) error {
	_, _ = fmt.Fprintf(out, "Amplitude ID: %d\n\n", a.AmplitudeID)
	if len(a.Events) == 0 {
		_, _ = fmt.Fprintln(out, "No events found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "TYPE\tTIME")
	for _, e := range a.Events {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", e.Type, e.Time)
	}
	return w.Flush()
}

func printAmplitudeCohortsTable(out io.Writer, cohorts []amplitude.Cohort) error {
	if len(cohorts) == 0 {
		_, _ = fmt.Fprintln(out, "No cohorts found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME\tSIZE\tARCHIVED\tDESCRIPTION")
	for _, c := range cohorts {
		size := "-"
		if c.Size != nil {
			size = fmt.Sprintf("%d", *c.Size)
		}
		archived := "no"
		if c.Archived {
			archived = "yes"
		}
		desc := cmdutil.TruncateRunes(c.Description, 60)
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, size, archived, desc)
	}
	return w.Flush()
}
