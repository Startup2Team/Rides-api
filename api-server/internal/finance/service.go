package finance

import (
	"context"
	"time"
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
