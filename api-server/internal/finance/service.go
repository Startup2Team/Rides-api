package finance

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetGeneralLedger(ctx context.Context, start, end *time.Time) ([]LedgerEntry, error) {
	txns, err := s.repo.GetWalletTransactions(ctx, start, end)
	if err != nil {
		return nil, err
	}

	payments, err := s.repo.GetPayments(ctx, start, end)
	if err != nil {
		return nil, err
	}

	var entries []LedgerEntry

	// 1. Process Wallet Transactions
	for _, t := range txns {
		switch t.Type {
		case "TOP_UP":
			// Debit: Cash (Asset)
			entries = append(entries, LedgerEntry{
				Date:          t.CreatedAt,
				TransactionID: t.ID,
				Account:       "Cash & Bank (MTN MoMo)",
				Description:   fmt.Sprintf("Driver Wallet Top-Up: %s", t.Description),
				Debit:         t.AmountRWF,
				Credit:        0,
				Reference:     t.ExternalRef,
			})
			// Credit: Driver Wallets (Liability)
			entries = append(entries, LedgerEntry{
				Date:          t.CreatedAt,
				TransactionID: t.ID,
				Account:       "Driver Wallets (Liability)",
				Description:   fmt.Sprintf("Driver Wallet Top-Up: %s", t.Description),
				Debit:         0,
				Credit:        t.AmountRWF,
				Reference:     t.ExternalRef,
			})
		case "WITHDRAW":
			// Debit: Driver Wallets (Liability)
			entries = append(entries, LedgerEntry{
				Date:          t.CreatedAt,
				TransactionID: t.ID,
				Account:       "Driver Wallets (Liability)",
				Description:   fmt.Sprintf("Payout Withdrawal: %s", t.Description),
				Debit:         t.AmountRWF,
				Credit:        0,
				Reference:     t.ExternalRef,
			})
			// Credit: Cash (Asset)
			entries = append(entries, LedgerEntry{
				Date:          t.CreatedAt,
				TransactionID: t.ID,
				Account:       "Cash & Bank (MTN MoMo)",
				Description:   fmt.Sprintf("Payout Withdrawal: %s", t.Description),
				Debit:         0,
				Credit:        t.AmountRWF,
				Reference:     t.ExternalRef,
			})
		case "PACKAGE_PURCHASE":
			// Debit: Driver Wallets (Liability)
			entries = append(entries, LedgerEntry{
				Date:          t.CreatedAt,
				TransactionID: t.ID,
				Account:       "Driver Wallets (Liability)",
				Description:   fmt.Sprintf("Package Subscription: %s", t.Description),
				Debit:         t.AmountRWF,
				Credit:        0,
				Reference:     t.ExternalRef,
			})
			// Credit: Package Sales Revenue (Revenue)
			entries = append(entries, LedgerEntry{
				Date:          t.CreatedAt,
				TransactionID: t.ID,
				Account:       "Package Sales Revenue",
				Description:   fmt.Sprintf("Package Subscription: %s", t.Description),
				Debit:         0,
				Credit:        t.AmountRWF,
				Reference:     t.ExternalRef,
			})
		}
	}

	// 2. Process Completed Ride Payments
	for _, p := range payments {
		desc := fmt.Sprintf("Ride payment completed (Ride %s)", p.RideID[:8])
		// Debit: Accounts Receivable (Asset)
		entries = append(entries, LedgerEntry{
			Date:          p.CreatedAt,
			TransactionID: p.ID,
			Account:       "Accounts Receivable",
			Description:   desc,
			Debit:         p.AmountRWF,
			Credit:        0,
			Reference:     p.RideID,
		})

		// Credit: Driver Wallets (Liability)
		entries = append(entries, LedgerEntry{
			Date:          p.CreatedAt,
			TransactionID: p.ID,
			Account:       "Driver Wallets (Liability)",
			Description:   fmt.Sprintf("Driver earnings - %s", desc),
			Debit:         0,
			Credit:        p.DriverAmountRWF,
			Reference:     p.RideID,
		})

		// Credit: Commission Revenue (Revenue)
		entries = append(entries, LedgerEntry{
			Date:          p.CreatedAt,
			TransactionID: p.ID,
			Account:       "Commission Revenue",
			Description:   fmt.Sprintf("Platform Commission - %s", desc),
			Debit:         0,
			Credit:        p.PlatformFeeRWF,
			Reference:     p.RideID,
		})
	}

	// Chronological Sort
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Date.Before(entries[j].Date)
	})

	return entries, nil
}

func (s *Service) GetTrialBalance(ctx context.Context, start, end *time.Time) (*TrialBalance, error) {
	entries, err := s.GetGeneralLedger(ctx, start, end)
	if err != nil {
		return nil, err
	}

	totals := make(map[string]*TrialBalanceRow)
	for _, e := range entries {
		row, ok := totals[e.Account]
		if !ok {
			row = &TrialBalanceRow{Account: e.Account}
			totals[e.Account] = row
		}
		row.DebitTotal += e.Debit
		row.CreditTotal += e.Credit
	}

	var rows []TrialBalanceRow
	var totalDebit, totalCredit int64
	for _, r := range totals {
		rows = append(rows, *r)
		totalDebit += r.DebitTotal
		totalCredit += r.CreditTotal
	}

	// Sort accounts alphabetically
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Account < rows[j].Account
	})

	return &TrialBalance{
		Rows:        rows,
		TotalDebit:  totalDebit,
		TotalCredit: totalCredit,
		Balanced:    totalDebit == totalCredit,
	}, nil
}

func (s *Service) GetBalanceSheet(ctx context.Context, asOf *time.Time) (*BalanceSheet, error) {
	var start *time.Time
	entries, err := s.GetGeneralLedger(ctx, start, asOf)
	if err != nil {
		return nil, err
	}

	accountBalances := make(map[string]int64)
	for _, e := range entries {
		// Asset / Expense increases with Debit, Liability / Equity / Revenue with Credit
		switch e.Account {
		case "Cash & Bank (MTN MoMo)", "Accounts Receivable":
			accountBalances[e.Account] += (e.Debit - e.Credit)
		case "Driver Wallets (Liability)":
			accountBalances[e.Account] += (e.Credit - e.Debit)
		case "Commission Revenue", "Package Sales Revenue":
			accountBalances[e.Account] += (e.Credit - e.Debit)
		}
	}

	var assets []BalanceSheetSection
	var totalAssets int64
	for _, acc := range []string{"Cash & Bank (MTN MoMo)", "Accounts Receivable"} {
		bal := accountBalances[acc]
		assets = append(assets, BalanceSheetSection{Account: acc, Balance: bal})
		totalAssets += bal
	}

	var liabilities []BalanceSheetSection
	var totalLiabilities int64
	for _, acc := range []string{"Driver Wallets (Liability)"} {
		bal := accountBalances[acc]
		liabilities = append(liabilities, BalanceSheetSection{Account: acc, Balance: bal})
		totalLiabilities += bal
	}

	// Equity: Retained Earnings is matching Net Revenue
	totalRevenue := accountBalances["Commission Revenue"] + accountBalances["Package Sales Revenue"]
	equity := []BalanceSheetSection{
		{Account: "Retained Earnings", Balance: totalRevenue},
	}
	totalEquity := totalRevenue

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

func fmtTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}
