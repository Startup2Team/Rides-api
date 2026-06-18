package wallet

import "time"

// TxType classifies every wallet movement.
type TxType string

const (
	TxTopUp           TxType = "TOP_UP"
	TxWithdraw        TxType = "WITHDRAW"
	TxPackagePurchase TxType = "PACKAGE_PURCHASE"
	TxCreditGrant     TxType = "CREDIT_GRANT"
	TxRefund          TxType = "REFUND"
)

// TxStatus reflects MoMo confirmation state.
type TxStatus string

const (
	StatusCompleted TxStatus = "COMPLETED"
	StatusPending   TxStatus = "PENDING"
	StatusFailed    TxStatus = "FAILED"
)

// Wallet is a user's balance record. One per user_id regardless of mode
// (customer or driver — same person). Balance is in integer RWF.
type Wallet struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	BalanceRWF int64     `json:"balance_rwf"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Transaction is an immutable audit-log entry for every balance change.
type Transaction struct {
	ID           string    `json:"id"`
	WalletID     string    `json:"wallet_id"`
	UserID       string    `json:"user_id"`
	Type         TxType    `json:"type"`
	AmountRWF    int64     `json:"amount_rwf"`
	BalanceAfter int64     `json:"balance_after"`
	Description  string    `json:"description,omitempty"`
	PhoneNumber  string    `json:"phone_number,omitempty"`
	ExternalRef  string    `json:"external_ref,omitempty"`
	Status       TxStatus  `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}
