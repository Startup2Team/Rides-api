package finance

import (
	"context"
	"strconv"
	"time"

	"github.com/workspace/ride-platform/internal/export"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// GetGeneralLedger returns the persisted double-entry journal over an optional
// date window. Every row is a real posted line — no read-time synthesis.
func (s *Service) GetGeneralLedger(ctx context.Context, start, end *time.Time) ([]LedgerEntry, error) {
	return s.repo.GetJournalEntries(ctx, start, end)
}

// GetTrialBalance aggregates posted lines per account. Balanced is now a genuine
// integrity check: it reflects the actual stored debit/credit sums, so a
// hand-edited or corrupt row would surface as an imbalance.
func (s *Service) GetTrialBalance(ctx context.Context, start, end *time.Time) (*TrialBalance, error) {
	rows, totalDebit, totalCredit, err := s.repo.GetTrialBalanceRows(ctx, start, end)
	if err != nil {
		return nil, err
	}
	return &TrialBalance{
		Rows:        rows,
		TotalDebit:  totalDebit,
		TotalCredit: totalCredit,
		Balanced:    totalDebit == totalCredit,
	}, nil
}

// GetBalanceSheet classifies account balances by accounting type. Assets =
// Σ(debit−credit) over asset accounts; liabilities/equity carry their natural
// (credit) sign as positive; period net income (revenue − expense) is folded
// into Retained Earnings. Assets == Liabilities + Equity holds by construction.
func (s *Service) GetBalanceSheet(ctx context.Context, asOf *time.Time) (*BalanceSheet, error) {
	balances, err := s.repo.GetAccountBalances(ctx, asOf)
	if err != nil {
		return nil, err
	}

	var assets, liabilities, equity []BalanceSheetSection
	var totalAssets, totalLiabilities, totalEquity, retainedEarnings int64

	for _, b := range balances {
		switch b.Type {
		case "ASSET":
			assets = append(assets, BalanceSheetSection{Account: b.Name, Balance: b.Balance})
			totalAssets += b.Balance
		case "LIABILITY":
			liabilities = append(liabilities, BalanceSheetSection{Account: b.Name, Balance: -b.Balance})
			totalLiabilities += -b.Balance
		case "EQUITY":
			equity = append(equity, BalanceSheetSection{Account: b.Name, Balance: -b.Balance})
			totalEquity += -b.Balance
		case "REVENUE", "EXPENSE":
			// Revenue (credit-normal) increases equity; expense (debit-normal)
			// reduces it. Both fold into retained earnings via -(debit-credit).
			retainedEarnings += -b.Balance
		}
	}

	equity = append(equity, BalanceSheetSection{Account: "Retained Earnings", Balance: retainedEarnings})
	totalEquity += retainedEarnings

	asOfTime := time.Now()
	if asOf != nil {
		asOfTime = *asOf
	}

	return &BalanceSheet{
		Assets:           assets,
		TotalAssets:      totalAssets,
		Liabilities:      liabilities,
		TotalLiabilities: totalLiabilities,
		Equity:           equity,
		TotalEquity:      totalEquity,
		AsOfDate:         asOfTime,
	}, nil
}

// ── Export tables (reused by the finance export handler + the reports builder) ──

// LedgerTable renders the general ledger as a format-neutral export table.
func (s *Service) LedgerTable(ctx context.Context, start, end *time.Time) (export.Table, error) {
	entries, err := s.GetGeneralLedger(ctx, start, end)
	if err != nil {
		return export.Table{}, err
	}
	t := export.Table{
		Title:   "General Ledger",
		Headers: []string{"Date", "Transaction ID", "Account", "Description", "Debit (RWF)", "Credit (RWF)", "Reference"},
	}
	for _, e := range entries {
		t.Rows = append(t.Rows, []string{
			e.Date.Format(time.RFC3339), e.TransactionID, e.Account, e.Description,
			strconv.FormatInt(e.Debit, 10), strconv.FormatInt(e.Credit, 10), e.Reference,
		})
	}
	return t, nil
}

// TrialBalanceTable renders the trial balance as an export table.
func (s *Service) TrialBalanceTable(ctx context.Context, start, end *time.Time) (export.Table, error) {
	tb, err := s.GetTrialBalance(ctx, start, end)
	if err != nil {
		return export.Table{}, err
	}
	t := export.Table{Title: "Trial Balance", Headers: []string{"Account", "Debit (RWF)", "Credit (RWF)"}}
	for _, row := range tb.Rows {
		t.Rows = append(t.Rows, []string{row.Account, strconv.FormatInt(row.DebitTotal, 10), strconv.FormatInt(row.CreditTotal, 10)})
	}
	t.Rows = append(t.Rows, []string{"TOTAL", strconv.FormatInt(tb.TotalDebit, 10), strconv.FormatInt(tb.TotalCredit, 10)})
	return t, nil
}

// BalanceSheetTable renders the balance sheet as an export table.
func (s *Service) BalanceSheetTable(ctx context.Context, asOf *time.Time) (export.Table, error) {
	bs, err := s.GetBalanceSheet(ctx, asOf)
	if err != nil {
		return export.Table{}, err
	}
	t := export.Table{Title: "Balance Sheet", Headers: []string{"Section", "Account", "Balance (RWF)"}}
	for _, x := range bs.Assets {
		t.Rows = append(t.Rows, []string{"Assets", x.Account, strconv.FormatInt(x.Balance, 10)})
	}
	t.Rows = append(t.Rows, []string{"Assets", "Total Assets", strconv.FormatInt(bs.TotalAssets, 10)})
	for _, x := range bs.Liabilities {
		t.Rows = append(t.Rows, []string{"Liabilities", x.Account, strconv.FormatInt(x.Balance, 10)})
	}
	t.Rows = append(t.Rows, []string{"Liabilities", "Total Liabilities", strconv.FormatInt(bs.TotalLiabilities, 10)})
	for _, x := range bs.Equity {
		t.Rows = append(t.Rows, []string{"Equity", x.Account, strconv.FormatInt(x.Balance, 10)})
	}
	t.Rows = append(t.Rows, []string{"Equity", "Total Equity", strconv.FormatInt(bs.TotalEquity, 10)})
	return t, nil
}

func (s *Service) GetStaffAnalytics(ctx context.Context) (*StaffAnalytics, error) {
	total, active, err := s.repo.GetStaffCount(ctx)
	if err != nil {
		return nil, err
	}

	actions, err := s.repo.GetTotalActions(ctx)
	if err != nil {
		return nil, err
	}

	activities, err := s.repo.GetStaffActivity(ctx)
	if err != nil {
		return nil, err
	}

	return &StaffAnalytics{
		TotalStaff:        total,
		ActiveAdmins:      active,
		ActionsCount:      actions,
		ActivityBreakdown: activities,
	}, nil
}
