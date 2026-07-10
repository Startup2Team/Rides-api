package finance

import "time"

type LedgerEntry struct {
	Date          time.Time `json:"date"`
	TransactionID string    `json:"transaction_id"`
	Account       string    `json:"account"`
	Description   string    `json:"description"`
	Debit         int64     `json:"debit"`
	Credit        int64     `json:"credit"`
	Reference     string    `json:"reference"`
}

type TrialBalanceRow struct {
	Account     string `json:"account"`
	DebitTotal  int64  `json:"debit_total"`
	CreditTotal int64  `json:"credit_total"`
}

type TrialBalance struct {
	Rows        []TrialBalanceRow `json:"rows"`
	TotalDebit  int64             `json:"total_debit"`
	TotalCredit int64             `json:"total_credit"`
	Balanced    bool              `json:"balanced"`
}

type BalanceSheetSection struct {
	Account string `json:"account"`
	Balance int64  `json:"balance"`
}

type BalanceSheet struct {
	Assets           []BalanceSheetSection `json:"assets"`
	TotalAssets      int64                 `json:"total_assets"`
	Liabilities      []BalanceSheetSection `json:"liabilities"`
	TotalLiabilities int64                 `json:"total_liabilities"`
	Equity           []BalanceSheetSection `json:"equity"`
	TotalEquity      int64                 `json:"total_equity"`
	AsOfDate         time.Time             `json:"as_of_date"`
}

type StaffActivity struct {
	AdminID     string     `json:"admin_id"`
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	ActionCount int        `json:"action_count"`
	LastActive  *time.Time `json:"last_active"`
}

type StaffAnalytics struct {
	TotalStaff        int             `json:"total_staff"`
	ActiveAdmins      int             `json:"active_admins"`
	ActionsCount      int             `json:"actions_count"`
	ActivityBreakdown []StaffActivity `json:"activity_breakdown"`
}
