package packages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// MoMoGateway is the minimal mobile-money interface the purchase flow needs.
// Implemented by an adapter over internal/payment in main. Nil = no live
// gateway (dev): purchases are auto-confirmed when DevAutoConfirm is set.
type MoMoGateway interface {
	RequestPayment(ctx context.Context, provider, phone string, amountRWF int, externalRef string) (txnID, status string, err error)
}

// Purchase mirrors a package_purchases row (immutable snapshot + lifecycle).
type Purchase struct {
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	PackageID         string     `json:"package_id"`
	PackageName       string     `json:"package_name"`
	PackageVersion    int        `json:"package_version"`
	CampaignCode      *string    `json:"campaign_code,omitempty"`
	PricePaidRWF      int        `json:"price_paid_rwf"`
	RidesGranted      int        `json:"rides_granted"`
	BonusRidesGranted int        `json:"bonus_rides_granted"`
	VehicleTypeCode   string     `json:"vehicle_type_code"`
	PaymentProvider   *string    `json:"payment_provider,omitempty"`
	PaymentRef        string     `json:"payment_ref"`
	CreatedAt         time.Time  `json:"created_at"`
	PaidAt            *time.Time `json:"paid_at,omitempty"`
}

// resolvedOffer is the effective offer for a package at purchase time.
type resolvedOffer struct {
	packageID, packageName         string
	vehicleTypeID, vehicleTypeCode string
	versionID                      string
	versionNumber                  int
	price, rides, bonus            int
	validityDays                   int
	isPromotional                  bool
	campaignID, campaignCode       *string
}

// PurchaseService runs the buy → MoMo → confirm → grant lifecycle.
type PurchaseService struct {
	repo           *Repository
	ledger         *LedgerService
	momo           MoMoGateway
	log            zerolog.Logger
	devAutoConfirm bool // dev only: simulate a successful MoMo callback inline
}

func NewPurchaseService(repo *Repository, ledger *LedgerService, momo MoMoGateway, devAutoConfirm bool, log zerolog.Logger) *PurchaseService {
	return &PurchaseService{repo: repo, ledger: ledger, momo: momo, devAutoConfirm: devAutoConfirm, log: log}
}

// resolveOffer picks the active version + best matching active campaign for a
// package. FIRST_PURCHASE campaigns only apply if the driver has no prior PAID
// purchase.
func (r *Repository) resolveOffer(ctx context.Context, packageID, driverProfileID string) (*resolvedOffer, error) {
	o := &resolvedOffer{packageID: packageID}
	err := r.db.QueryRow(ctx, `
		SELECT p.name, p.vehicle_type_id, vt.code,
		       v.id, v.version_number, v.rides, v.bonus_rides, v.price_rwf, v.validity_days, v.is_promotional
		FROM ride_packages p
		JOIN vehicle_types vt ON vt.id = p.vehicle_type_id
		JOIN ride_package_versions v ON v.package_id = p.id AND v.status = 'ACTIVE'
		WHERE p.id = $1 AND p.is_active = TRUE
	`, packageID).Scan(
		&o.packageName, &o.vehicleTypeID, &o.vehicleTypeCode,
		&o.versionID, &o.versionNumber, &o.rides, &o.bonus, &o.price, &o.validityDays, &o.isPromotional,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.New(http.StatusNotFound, "PACKAGE_NOT_FOUND", "package not available")
		}
		return nil, err
	}

	var hasPriorPurchase bool
	_ = r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM package_purchases WHERE driver_id=$1 AND status='PAID')`, driverProfileID).Scan(&hasPriorPurchase)

	var (
		ovPrice, ovRides, ovBonus *int
		cID, cCode                *string
	)
	err = r.db.QueryRow(ctx, `
		SELECT c.id, c.code, c.override_price_rwf, c.override_rides, c.override_bonus_rides
		FROM campaigns c
		WHERE c.status='ACTIVE'
		  AND (c.starts_at IS NULL OR c.starts_at <= now())
		  AND (c.ends_at   IS NULL OR c.ends_at   >= now())
		  AND (c.target_vehicle_type_id IS NULL OR c.target_vehicle_type_id = $2)
		  AND (c.target_package_id      IS NULL OR c.target_package_id      = $1)
		  AND (c.type <> 'FIRST_PURCHASE' OR $3 = FALSE)
		ORDER BY c.priority DESC, c.created_at DESC
		LIMIT 1
	`, packageID, o.vehicleTypeID, hasPriorPurchase).Scan(&cID, &cCode, &ovPrice, &ovRides, &ovBonus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if cID != nil {
		o.campaignID, o.campaignCode = cID, cCode
		if ovPrice != nil {
			o.price = *ovPrice
		}
		if ovRides != nil {
			o.rides = *ovRides
		}
		if ovBonus != nil {
			o.bonus = *ovBonus
		}
	}
	return o, nil
}

// createPurchaseRow inserts a package_purchases row with the frozen snapshot.
func (r *Repository) createPurchaseRow(ctx context.Context, driverProfileID string, vehicleID *string, o *resolvedOffer, status, provider, phone, paymentRef, idempotencyKey string, expiresAt *time.Time) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO package_purchases
		    (driver_id, vehicle_id, vehicle_type_id, package_id, package_version_id, package_version_number,
		     package_name, campaign_id, campaign_code, price_paid_rwf, rides_granted, bonus_rides_granted,
		     is_unlimited, status, payment_provider, payment_phone, payment_ref, idempotency_key, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,FALSE,$13,$14,$15,$16,$17,$18)
		RETURNING id
	`, driverProfileID, vehicleID, o.vehicleTypeID, o.packageID, o.versionID, o.versionNumber,
		o.packageName, o.campaignID, o.campaignCode, o.price, o.rides, o.bonus,
		status, nullStr(provider), nullStr(phone), paymentRef, idempotencyKey, expiresAt).Scan(&id)
	return id, err
}

// markCreditsGranted flips the crash-recovery flag once the ledger grant lands.
func (r *Repository) markCreditsGranted(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE package_purchases SET credits_granted=TRUE, updated_at=now() WHERE id=$1`, id)
	return err
}

// storeWebhookPayload keeps the raw MTN callback as immutable evidence.
func (r *Repository) storeWebhookPayload(ctx context.Context, paymentRef string, payload []byte) {
	if len(payload) == 0 {
		return
	}
	_, _ = r.db.Exec(ctx, `UPDATE package_purchases SET webhook_payload=$2::jsonb, updated_at=now() WHERE payment_ref=$1`, paymentRef, string(payload))
}

// CreateInput is the purchase request.
type CreateInput struct {
	PackageID      string `json:"package_id" validate:"required,uuid"`
	IdempotencyKey string `json:"idempotency_key" validate:"required"`
	MomoPhone      string `json:"momo_phone"`
	MomoProvider   string `json:"momo_provider"` // mtn | airtel
}

// Create starts a purchase: resolves the offer, snapshots it, and either grants
// immediately (free/promotional) or opens a PENDING MoMo charge. Idempotent on
// idempotency_key.
func (s *PurchaseService) devSettlePending(ctx context.Context, purchaseID, paymentRef string) (*Purchase, error) {
	if !s.devAutoConfirm || paymentRef == "" {
		return nil, nil
	}
	if err := s.confirm(ctx, paymentRef, "DEV-SIMULATED", true); err != nil {
		return nil, err
	}
	return s.repo.getPurchaseByID(ctx, purchaseID)
}

func (s *PurchaseService) Create(ctx context.Context, userID string, in CreateInput) (*Purchase, error) {
	// Idempotency: return the existing purchase if this key was already used.
	if existing, err := s.repo.getPurchaseByIdem(ctx, in.IdempotencyKey); err == nil && existing != nil {
		if existing.Status == "PENDING" {
			if settled, settleErr := s.devSettlePending(ctx, existing.ID, existing.PaymentRef); settleErr != nil {
				s.log.Warn().Err(settleErr).Str("purchase_id", existing.ID).Msg("packages: dev auto-confirm on idempotent purchase failed")
			} else if settled != nil {
				return settled, nil
			}
		}
		return existing, nil
	}

	profileID, _, err := s.repo.driverProfileAndActiveVehicle(ctx, userID)
	if err != nil {
		s.log.Error().Err(err).Str("step", "resolve_profile").Msg("purchase create failed")
		return nil, err
	}
	offer, err := s.repo.resolveOffer(ctx, in.PackageID, profileID)
	if err != nil {
		s.log.Error().Err(err).Str("step", "resolve_offer").Msg("purchase create failed")
		return nil, err
	}
	// Active vehicle of the package's type (nullable).
	var vehicleID *string
	_ = s.repo.db.QueryRow(ctx, `
		SELECT id FROM driver_vehicles WHERE driver_id=$1 AND vehicle_type_id=$2 AND is_active=TRUE ORDER BY created_at LIMIT 1
	`, profileID, offer.vehicleTypeID).Scan(&vehicleID)

	paymentRef := uuid.NewString()
	expiresAt := time.Now().Add(time.Duration(offer.validityDays) * 24 * time.Hour)

	// Free / promotional → grant immediately, no MoMo (no payment timeout).
	if offer.price <= 0 || offer.isPromotional {
		id, err := s.repo.createPurchaseRow(ctx, profileID, vehicleID, offer, "PAID", "", "", paymentRef, in.IdempotencyKey, nil)
		if err != nil {
			return nil, err
		}
		if err := s.repo.markPaid(ctx, id, ""); err != nil {
			return nil, err
		}
		if err := s.ledger.GrantPurchase(ctx, profileID, vehicleID, offer.vehicleTypeID, id, offer.rides, offer.bonus, expiresAt); err != nil {
			s.log.Error().Err(err).Str("purchase_id", id).Msg("packages: free grant failed")
		} else {
			_ = s.repo.markCreditsGranted(ctx, id)
		}
		return s.repo.getPurchaseByID(ctx, id)
	}

	// Paid → PENDING + MoMo prompt. 5-minute payment window.
	paymentExpiry := time.Now().Add(5 * time.Minute)
	id, err := s.repo.createPurchaseRow(ctx, profileID, vehicleID, offer, "PENDING", in.MomoProvider, in.MomoPhone, paymentRef, in.IdempotencyKey, &paymentExpiry)
	if err != nil {
		s.log.Error().Err(err).Str("step", "create_purchase_row").Msg("purchase create failed")
		return nil, err
	}
	// Dev: skip live MoMo — auto-confirm inline so mobile does not poll forever.
	if !s.devAutoConfirm && s.momo != nil && in.MomoPhone != "" {
		provider := "MTN_MOMO"
		if in.MomoProvider == "airtel" {
			provider = "AIRTEL_MONEY"
		}
		if _, _, err := s.momo.RequestPayment(ctx, provider, in.MomoPhone, offer.price, paymentRef); err != nil {
			s.log.Warn().Err(err).Str("purchase_id", id).Msg("packages: momo prompt failed")
		}
	}
	if settled, err := s.devSettlePending(ctx, id, paymentRef); err != nil {
		s.log.Error().Err(err).Str("purchase_id", id).Msg("packages: dev auto-confirm failed")
		return nil, fmt.Errorf("dev auto-confirm: %w", err)
	} else if settled != nil {
		return settled, nil
	}
	return s.repo.getPurchaseByID(ctx, id)
}

// Confirm is the MoMo webhook entry point. Idempotent on the purchase status
// guard. rawPayload is the verbatim provider callback, stored as evidence.
func (s *PurchaseService) Confirm(ctx context.Context, paymentRef, providerTxnID string, success bool, rawPayload []byte) error {
	s.repo.storeWebhookPayload(ctx, paymentRef, rawPayload)
	return s.confirm(ctx, paymentRef, providerTxnID, success)
}

func (s *PurchaseService) confirm(ctx context.Context, paymentRef, providerTxnID string, success bool) error {
	p, profileID, vehicleID, vehicleTypeID, rides, bonus, validityDays, status, err := s.repo.lockPurchaseForConfirm(ctx, paymentRef)
	if err != nil {
		return err
	}
	if status != "PENDING" {
		return nil // already settled — idempotent
	}
	if !success {
		return s.repo.markFailed(ctx, p)
	}
	if err := s.repo.markPaid(ctx, p, providerTxnID); err != nil {
		return err
	}
	expiresAt := time.Now().Add(time.Duration(validityDays) * 24 * time.Hour)
	if err := s.ledger.GrantPurchase(ctx, profileID, vehicleID, vehicleTypeID, p, rides, bonus, expiresAt); err != nil {
		return fmt.Errorf("grant after payment: %w", err)
	}
	_ = s.repo.markCreditsGranted(ctx, p)
	return nil
}

// GetStatus returns a purchase for polling (must belong to the user).
func (s *PurchaseService) GetStatus(ctx context.Context, userID, purchaseID string) (*Purchase, error) {
	p, err := s.repo.getPurchaseForUser(ctx, userID, purchaseID)
	if err != nil {
		return nil, err
	}
	if p.Status == "PENDING" {
		if settled, settleErr := s.devSettlePending(ctx, p.ID, p.PaymentRef); settleErr != nil {
			s.log.Warn().Err(settleErr).Str("purchase_id", p.ID).Msg("packages: dev auto-confirm on status poll failed")
		} else if settled != nil {
			return settled, nil
		}
	}
	return p, nil
}

// History lists a driver's purchases, newest first.
func (s *PurchaseService) History(ctx context.Context, userID string) ([]*Purchase, error) {
	return s.repo.listPurchasesForUser(ctx, userID)
}

type AdminPurchase struct {
	ID                string     `json:"id"`
	DriverID          string     `json:"driver_id"`
	DriverName        string     `json:"driver_name"`
	DriverPhone       string     `json:"driver_phone"`
	VehicleID         *string    `json:"vehicle_id,omitempty"`
	VehicleTypeCode   string     `json:"vehicle_type_code"`
	VehiclePlate      string     `json:"vehicle_plate"`
	PackageID         string     `json:"package_id"`
	PackageName       string     `json:"package_name"`
	PackageVersion    int        `json:"package_version"`
	CampaignID        *string    `json:"campaign_id,omitempty"`
	CampaignCode      *string    `json:"campaign_code,omitempty"`
	CampaignName      *string    `json:"campaign_name,omitempty"`
	PricePaidRWF      int        `json:"price_paid_rwf"`
	RidesGranted      int        `json:"rides_granted"`
	BonusRidesGranted int        `json:"bonus_rides_granted"`
	Status            string     `json:"status"`
	PaymentProvider   *string    `json:"payment_provider,omitempty"`
	PaymentRef        string     `json:"payment_ref"`
	CreatedAt         time.Time  `json:"created_at"`
	PaidAt            *time.Time `json:"paid_at,omitempty"`
}

// ListAllPurchases lists all package purchases for the admin, newest first.
func (s *PurchaseService) ListAllPurchases(ctx context.Context) ([]*AdminPurchase, error) {
	return s.repo.listAllPurchases(ctx)
}

// ── purchase repository methods ──────────────────────────────────────────────

const purchaseSelectCols = `
	pp.id, pp.status, pp.package_id, pp.package_name, pp.package_version_number,
	pp.campaign_code, pp.price_paid_rwf, pp.rides_granted, pp.bonus_rides_granted,
	vt.code, pp.payment_provider, pp.payment_ref, pp.created_at, pp.paid_at`

func scanPurchase(row pgx.Row) (*Purchase, error) {
	p := &Purchase{}
	if err := row.Scan(
		&p.ID, &p.Status, &p.PackageID, &p.PackageName, &p.PackageVersion,
		&p.CampaignCode, &p.PricePaidRWF, &p.RidesGranted, &p.BonusRidesGranted,
		&p.VehicleTypeCode, &p.PaymentProvider, &p.PaymentRef, &p.CreatedAt, &p.PaidAt,
	); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *Repository) purchaseBaseQuery() string {
	return `SELECT ` + purchaseSelectCols + `
		FROM package_purchases pp
		JOIN vehicle_types vt ON vt.id = pp.vehicle_type_id`
}

func (r *Repository) getPurchaseByID(ctx context.Context, id string) (*Purchase, error) {
	return scanPurchase(r.db.QueryRow(ctx, r.purchaseBaseQuery()+` WHERE pp.id = $1`, id))
}

func (r *Repository) getPurchaseByIdem(ctx context.Context, idemKey string) (*Purchase, error) {
	p, err := scanPurchase(r.db.QueryRow(ctx, r.purchaseBaseQuery()+` WHERE pp.idempotency_key = $1`, idemKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (r *Repository) getPurchaseForUser(ctx context.Context, userID, purchaseID string) (*Purchase, error) {
	p, err := scanPurchase(r.db.QueryRow(ctx, r.purchaseBaseQuery()+`
		JOIN driver_profiles dp ON dp.id = pp.driver_id
		WHERE pp.id = $1 AND dp.user_id = $2`, purchaseID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperrors.ErrNotFound
	}
	return p, err
}

func (r *Repository) listPurchasesForUser(ctx context.Context, userID string) ([]*Purchase, error) {
	rows, err := r.db.Query(ctx, r.purchaseBaseQuery()+`
		JOIN driver_profiles dp ON dp.id = pp.driver_id
		WHERE dp.user_id = $1
		ORDER BY pp.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Purchase{}
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) listAllPurchases(ctx context.Context) ([]*AdminPurchase, error) {
	query := `
		SELECT pp.id, pp.driver_id, u.full_name, u.phone_number, pp.vehicle_id, vt.code, COALESCE(dp.vehicle_plate, ''),
		       pp.package_id, pp.package_name, pp.package_version_number,
		       pp.campaign_id, pp.campaign_code, c.name,
		       pp.price_paid_rwf, pp.rides_granted, pp.bonus_rides_granted,
		       pp.status, pp.payment_provider, pp.payment_ref, pp.created_at, pp.paid_at
		FROM package_purchases pp
		JOIN driver_profiles dp ON dp.id = pp.driver_id
		JOIN users u ON u.id = dp.user_id
		JOIN vehicle_types vt ON vt.id = pp.vehicle_type_id
		LEFT JOIN campaigns c ON c.id = pp.campaign_id
		ORDER BY pp.created_at DESC`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AdminPurchase
	for rows.Next() {
		ap := &AdminPurchase{}
		err := rows.Scan(
			&ap.ID, &ap.DriverID, &ap.DriverName, &ap.DriverPhone, &ap.VehicleID, &ap.VehicleTypeCode, &ap.VehiclePlate,
			&ap.PackageID, &ap.PackageName, &ap.PackageVersion,
			&ap.CampaignID, &ap.CampaignCode, &ap.CampaignName,
			&ap.PricePaidRWF, &ap.RidesGranted, &ap.BonusRidesGranted,
			&ap.Status, &ap.PaymentProvider, &ap.PaymentRef, &ap.CreatedAt, &ap.PaidAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, ap)
	}
	return out, rows.Err()
}

// driverProfileAndActiveVehicle maps an auth user_id to driver_profiles.id.
// The active vehicle is resolved separately from driver_vehicles by the caller
// (the live schema has no active_vehicle_id column), so the second return is nil.
func (r *Repository) driverProfileAndActiveVehicle(ctx context.Context, userID string) (string, *string, error) {
	var profileID string
	err := r.db.QueryRow(ctx, `SELECT id FROM driver_profiles WHERE user_id = $1`, userID).Scan(&profileID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, apperrors.New(http.StatusForbidden, "NOT_A_DRIVER", "no driver profile for this account")
		}
		return "", nil, err
	}
	return profileID, nil, nil
}

func (r *Repository) markPaid(ctx context.Context, id, providerTxnID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE package_purchases
		SET status='PAID', provider_txn_id=COALESCE(NULLIF($2,''), provider_txn_id), paid_at=now(), updated_at=now()
		WHERE id = $1`, id, providerTxnID)
	return err
}

func (r *Repository) markFailed(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE package_purchases SET status='FAILED', updated_at=now() WHERE id=$1`, id)
	return err
}

// lockPurchaseForConfirm loads the snapshot needed to grant credits on a
// successful payment. Returns the purchase id, driver profile, vehicle, vehicle
// type, granted rides/bonus, version validity, and current status.
func (r *Repository) lockPurchaseForConfirm(ctx context.Context, paymentRef string) (id, profileID string, vehicleID *string, vehicleTypeID string, rides, bonus, validityDays int, status string, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT pp.id, pp.driver_id, pp.vehicle_id, pp.vehicle_type_id,
		       pp.rides_granted, pp.bonus_rides_granted, COALESCE(v.validity_days, 30), pp.status
		FROM package_purchases pp
		LEFT JOIN ride_package_versions v ON v.id = pp.package_version_id
		WHERE pp.payment_ref = $1`, paymentRef).Scan(
		&id, &profileID, &vehicleID, &vehicleTypeID, &rides, &bonus, &validityDays, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = apperrors.ErrNotFound
	}
	return
}
