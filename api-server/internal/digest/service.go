package digest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// StorageChecker lets the digest report whether object storage is actually
// reachable, so "0 documents uploaded" can be told apart from "uploads are
// broken" — a distinction that cost weeks the last time it was missing.
type StorageChecker interface {
	CheckStorage(ctx context.Context) error
}

// Sender is the subset of the Telegram notifier the digest needs.
type Sender interface {
	Notify(text string)
}

type Service struct {
	repo    *Repository
	sender  Sender
	storage StorageChecker
	env     string
	loc     *time.Location
	hour    int
	log     zerolog.Logger
	now     func() time.Time
}

type Options struct {
	Env      string
	Timezone string // IANA name; falls back to UTC with a warning
	Hour     int    // local hour of day to send, 0–23
}

func NewService(repo *Repository, sender Sender, storage StorageChecker, opts Options, log zerolog.Logger) *Service {
	loc, err := time.LoadLocation(opts.Timezone)
	if err != nil || opts.Timezone == "" {
		if opts.Timezone != "" {
			log.Warn().Str("timezone", opts.Timezone).Msg("digest: unknown timezone, falling back to UTC")
		}
		loc = time.UTC
	}
	hour := opts.Hour
	if hour < 0 || hour > 23 {
		hour = 7
	}
	return &Service{
		repo: repo, sender: sender, storage: storage,
		env: opts.Env, loc: loc, hour: hour, log: log, now: time.Now,
	}
}

// Start schedules the digest for the configured local hour, every day.
//
// It sleeps to the next occurrence rather than ticking hourly, so a restart
// never double-sends and the send time does not drift.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.sender == nil {
		return
	}
	go func() {
		for {
			wait := s.untilNextRun()
			s.log.Info().
				Str("at", s.now().In(s.loc).Add(wait).Format(time.RFC1123)).
				Msg("digest: next daily summary scheduled")
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			s.SendDaily(ctx)
		}
	}()
}

func (s *Service) untilNextRun() time.Duration {
	now := s.now().In(s.loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), s.hour, 0, 0, 0, s.loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// SendDaily builds and pushes yesterday's summary. Failures are logged at Error
// (which alerts) rather than returned — nothing upstream can act on them.
func (s *Service) SendDaily(ctx context.Context) {
	text, err := s.Build(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("digest: failed to build daily summary")
		return
	}
	s.sender.Notify(text)
}

// Build renders the summary for yesterday. Exported so the /stats command can
// reuse exactly the same numbers the morning message reports.
func (s *Service) Build(ctx context.Context) (string, error) {
	yesterday := s.now().In(s.loc).AddDate(0, 0, -1)
	snap, err := s.repo.Collect(ctx, yesterday, s.loc)
	if err != nil {
		return "", err
	}
	if s.storage != nil {
		if serr := s.storage.CheckStorage(ctx); serr != nil {
			snap.StorageErr = serr.Error()
		} else {
			snap.StorageOK = true
		}
	} else {
		snap.StorageErr = "storage not configured"
	}
	return Format(snap, s.env), nil
}

// BuildPending answers /pending: only the things waiting on a human, so it can
// be checked repeatedly through the day without wading past yesterday's stats.
func (s *Service) BuildPending(ctx context.Context) (string, error) {
	snap, err := s.repo.Collect(ctx, s.now().In(s.loc).AddDate(0, 0, -1), s.loc)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("❗ Waiting on someone\n────────────────────\n")
	any := false
	for _, row := range []struct {
		n     int
		label string
	}{
		{snap.PendingApplications, "driver application(s) awaiting review"},
		{snap.ExpiringDocuments, "driver(s) with documents expiring in 30 days"},
		{snap.OpenTickets, "open support ticket(s)"},
		{snap.OpenIncidents, "open safety incident(s)"},
	} {
		if row.n > 0 {
			fmt.Fprintf(&b, "• %d %s\n", row.n, row.label)
			any = true
		}
	}
	if !any {
		b.WriteString("Nothing pending. 🎉\n")
	}
	return b.String(), nil
}

// Format turns a snapshot into the Telegram message. Plain text: the notifier
// posts without a parse_mode, so Markdown would leak literal asterisks.
func Format(s *Snapshot, env string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "📊 Rides daily — %s (%s)\n", s.Day.Format("Mon 2 Jan 2006"), strings.ToUpper(env))
	b.WriteString("────────────────────\n")

	fmt.Fprintf(&b, "\n🚗 Rides\n")
	fmt.Fprintf(&b, "  Completed   %d %s\n", s.RidesCompleted, delta(s.RidesCompleted, s.RidesCompletedPrev))
	fmt.Fprintf(&b, "  Requested   %d\n", s.RidesRequested)
	fmt.Fprintf(&b, "  Cancelled   %d%s\n", s.RidesCancelled, cancelRate(s.RidesCancelled, s.RidesRequested))
	fmt.Fprintf(&b, "  Fares       %s RWF %s\n", money(s.FareRWF), delta64(s.FareRWF, s.FarePrevRWF))

	fmt.Fprintf(&b, "\n📈 Growth\n")
	fmt.Fprintf(&b, "  New users   %d  (drivers %d)\n", s.NewCustomers, s.NewDrivers)
	fmt.Fprintf(&b, "  Total users %s\n", money(int64(s.TotalUsers)))

	fmt.Fprintf(&b, "\n💰 Packages\n")
	fmt.Fprintf(&b, "  Sold        %d\n", s.PackagesSold)
	fmt.Fprintf(&b, "  Revenue     %s RWF\n", money(s.PackageRevenue))
	if len(s.PaymentsByState) > 0 {
		fmt.Fprintf(&b, "  Payments    %s\n", statuses(s.PaymentsByState))
	}

	fmt.Fprintf(&b, "\n🧑‍✈️ Drivers\n")
	fmt.Fprintf(&b, "  Approved    %d  (online now %d)\n", s.ApprovedDrivers, s.OnlineNow)
	fmt.Fprintf(&b, "  Docs added  %d\n", s.DocumentsUploaded)

	// Everything below is a to-do list, not a statistic. Only shown when it
	// needs a human, so an all-clear morning stays short and skimmable.
	var actions []string
	if s.PendingApplications > 0 {
		actions = append(actions, fmt.Sprintf("  ⚠️ %d driver application(s) awaiting review", s.PendingApplications))
	}
	if s.ExpiringDocuments > 0 {
		actions = append(actions, fmt.Sprintf("  ⚠️ %d driver(s) with documents expiring in 30 days", s.ExpiringDocuments))
	}
	if s.OpenTickets > 0 {
		actions = append(actions, fmt.Sprintf("  ⚠️ %d open support ticket(s)", s.OpenTickets))
	}
	if s.OpenIncidents > 0 {
		actions = append(actions, fmt.Sprintf("  🚨 %d open safety incident(s)", s.OpenIncidents))
	}
	if !s.StorageOK {
		actions = append(actions, fmt.Sprintf("  🔴 Object storage unreachable — uploads are failing (%s)", truncate(s.StorageErr, 120)))
	}
	if len(actions) > 0 {
		b.WriteString("\n❗ Needs attention\n")
		b.WriteString(strings.Join(actions, "\n"))
		b.WriteString("\n")
	} else {
		b.WriteString("\n✅ Nothing needs attention\n")
	}

	fmt.Fprintf(&b, "\n🩺 Platform\n")
	fmt.Fprintf(&b, "  Storage     %s\n", okLabel(s.StorageOK))
	fmt.Fprintf(&b, "  Database    %s\n", s.DBSize)

	return b.String()
}

func okLabel(ok bool) string {
	if ok {
		return "ok"
	}
	return "UNREACHABLE"
}

// delta renders a day-over-day change. Empty when there is no prior day to
// compare against, rather than a meaningless "+100%".
func delta(cur, prev int) string {
	if prev == 0 {
		if cur == 0 {
			return ""
		}
		return "(new)"
	}
	diff := cur - prev
	switch {
	case diff > 0:
		return fmt.Sprintf("(▲%d, +%d%%)", diff, diff*100/prev)
	case diff < 0:
		return fmt.Sprintf("(▼%d, %d%%)", -diff, diff*100/prev)
	default:
		return "(=)"
	}
}

// delta64 is delta for money: the absolute change is thousands-separated too,
// because "(▲33500)" next to "184,500 RWF" reads as a different magnitude.
func delta64(cur, prev int64) string {
	if prev == 0 {
		if cur == 0 {
			return ""
		}
		return "(new)"
	}
	diff := cur - prev
	pct := diff * 100 / prev
	switch {
	case diff > 0:
		return fmt.Sprintf("(▲%s, +%d%%)", money(diff), pct)
	case diff < 0:
		return fmt.Sprintf("(▼%s, %d%%)", money(-diff), pct)
	default:
		return "(=)"
	}
}

func cancelRate(cancelled, requested int) string {
	if requested == 0 || cancelled == 0 {
		return ""
	}
	return fmt.Sprintf("  (%d%% of requests)", cancelled*100/requested)
}

// money renders 1234567 as "1,234,567" — thousands separators, since RWF
// amounts are routinely six or seven digits.
func money(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	digits := fmt.Sprintf("%d", v)
	var out []byte
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// statuses renders the payment breakdown in a stable order, so the same day's
// message is byte-identical if regenerated (Go map order is randomised).
func statuses(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", strings.ToLower(k), m[k]))
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
