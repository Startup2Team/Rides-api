package wallet

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
)

const (
	maxTopUpRWF    int64 = 500_000 // 500k RWF cap per top-up
	maxWithdrawRWF int64 = 500_000
	minAmountRWF   int64 = 100
)

// Service contains wallet business logic.
type Service struct {
	repo *Repository
	log  zerolog.Logger
}

func NewService(repo *Repository, log zerolog.Logger) *Service {
	return &Service{repo: repo, log: log}
}

// GetWallet returns the wallet for a user (auto-created on user registration via DB trigger).
func (s *Service) GetWallet(ctx context.Context, userID string) (*Wallet, error) {
	return s.repo.GetByUserID(ctx, userID)
}

// GetTransactions returns paginated transaction history for a user.
func (s *Service) GetTransactions(ctx context.Context, userID string, limit, offset int) ([]*Transaction, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.repo.ListTransactions(ctx, userID, limit, offset)
}

// TopUp adds money to the wallet from a phone number.
// Without a MoMo gateway the call completes immediately (COMPLETED status).
// When the gateway is integrated, this will initiate a MoMo collect request
// and return PENDING — a webhook will later call ConfirmTopUp.
func (s *Service) TopUp(ctx context.Context, userID string, amountRWF int64, phoneNumber string) (*Transaction, error) {
	if err := validateAmount(amountRWF, maxTopUpRWF); err != nil {
		return nil, err
	}
	desc := fmt.Sprintf("Top-up from %s", phoneNumber)
	t, err := s.repo.TopUp(ctx, userID, amountRWF, phoneNumber, "", desc)
	if err != nil {
		return nil, err
	}
	s.log.Info().
		Str("user_id", userID).
		Int64("amount_rwf", amountRWF).
		Str("phone", phoneNumber).
		Msg("wallet: top-up completed")
	return t, nil
}

// Withdraw deducts money from the wallet to a phone number.
func (s *Service) Withdraw(ctx context.Context, userID string, amountRWF int64, phoneNumber string) (*Transaction, error) {
	if err := validateAmount(amountRWF, maxWithdrawRWF); err != nil {
		return nil, err
	}
	desc := fmt.Sprintf("Withdrawal to %s", phoneNumber)
	t, err := s.repo.Withdraw(ctx, userID, amountRWF, phoneNumber, "", desc)
	if err != nil {
		return nil, err
	}
	s.log.Info().
		Str("user_id", userID).
		Int64("amount_rwf", amountRWF).
		Str("phone", phoneNumber).
		Msg("wallet: withdrawal completed")
	return t, nil
}

// DeductForPackage deducts a package price from the wallet.
// Called by the packages service when a driver buys a package.
// Returns interface{} so packages.WalletDeductor can be satisfied without an import cycle.
func (s *Service) DeductForPackage(ctx context.Context, userID string, amountRWF int64, packageName string) (interface{}, error) {
	desc := fmt.Sprintf("Package purchase: %s", packageName)
	return s.repo.DeductForPackage(ctx, userID, amountRWF, desc)
}

// AdminCreditGrant lets admin add funds to any user's wallet (promo, support, etc.).
func (s *Service) AdminCreditGrant(ctx context.Context, userID string, amountRWF int64, reason string) (*Transaction, error) {
	if amountRWF <= 0 {
		return nil, fmt.Errorf("wallet: grant amount must be positive")
	}
	desc := fmt.Sprintf("Admin credit: %s", reason)
	return s.repo.CreditGrant(ctx, userID, amountRWF, desc)
}

func validateAmount(amount, max int64) error {
	if amount < minAmountRWF {
		return fmt.Errorf("wallet: minimum amount is %d RWF", minAmountRWF)
	}
	if amount > max {
		return fmt.Errorf("wallet: maximum amount is %d RWF", max)
	}
	return nil
}
