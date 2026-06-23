package packages

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Repository handles all credit and package database operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ListPackages returns active packages for a given vehicle type code.
func (r *Repository) ListPackages(ctx context.Context, vehicleTypeCode string) ([]*Package, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rp.id, rp.name, rp.vehicle_type_id, vt.code,
		       rp.ride_count, rp.bonus_rides, rp.validity_days, rp.price_rwf,
		       rp.is_promotional, rp.is_active, rp.created_at, rp.deleted_at
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE vt.code = $1
		  AND rp.is_active = TRUE
		  AND rp.deleted_at IS NULL
		ORDER BY rp.price_rwf ASC
	`, vehicleTypeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pkgs []*Package
	for rows.Next() {
		p := &Package{}
		if err := rows.Scan(
			&p.ID, &p.Name, &p.VehicleTypeID, &p.VehicleTypeCode,
			&p.RideCount, &p.BonusRides, &p.ValidityDays, &p.PriceRWF,
			&p.IsPromotional, &p.IsActive, &p.CreatedAt, &p.DeletedAt,
		); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, rows.Err()
}

// ListAllPackages returns all packages (active and inactive) for the admin panel, newest first.
func (r *Repository) ListAllPackages(ctx context.Context) ([]*Package, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rp.id, rp.name, rp.vehicle_type_id, vt.code,
		       rp.ride_count, rp.bonus_rides, rp.validity_days, rp.price_rwf,
		       rp.is_promotional, rp.is_active, rp.created_at, rp.deleted_at
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE rp.deleted_at IS NULL
		ORDER BY vt.code, rp.price_rwf ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pkgs []*Package
	for rows.Next() {
		p := &Package{}
		if err := rows.Scan(
			&p.ID, &p.Name, &p.VehicleTypeID, &p.VehicleTypeCode,
			&p.RideCount, &p.BonusRides, &p.ValidityDays, &p.PriceRWF,
			&p.IsPromotional, &p.IsActive, &p.CreatedAt, &p.DeletedAt,
		); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, rows.Err()
}

// CreatePackage inserts a new admin-defined package and creates version 1.
func (r *Repository) CreatePackage(ctx context.Context, input *CreatePackageInput) (*Package, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var vehicleTypeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1 AND is_active = TRUE`, input.VehicleTypeCode).Scan(&vehicleTypeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.New(404, "VEHICLE_TYPE_NOT_FOUND", "vehicle type not found")
		}
		return nil, err
	}

	p := &Package{}
	packageCode := packageCodeFromName(input.Name)
	err = tx.QueryRow(ctx, `
		INSERT INTO ride_packages (name, code, vehicle_type_id, ride_count, bonus_rides, validity_days, price_rwf, cost_per_ride_rwf, is_promotional)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, name, vehicle_type_id, ride_count, bonus_rides, validity_days, price_rwf, is_promotional, is_active, created_at, deleted_at
	`, input.Name, packageCode, vehicleTypeID, input.RideCount, input.BonusRides, input.ValidityDays, input.PriceRWF, input.CostPerRideRWF, input.IsPromotional).Scan(
		&p.ID, &p.Name, &p.VehicleTypeID, &p.RideCount, &p.BonusRides,
		&p.ValidityDays, &p.PriceRWF, &p.IsPromotional, &p.IsActive, &p.CreatedAt, &p.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	p.VehicleTypeCode = input.VehicleTypeCode

	// Insert version 1 for versioned purchases
	_, err = tx.Exec(ctx, `
		INSERT INTO ride_package_versions (package_id, version_number, rides, bonus_rides, price_rwf, cost_per_ride_rwf, validity_days, is_promotional, status, active_from)
		VALUES ($1, 1, $2, $3, $4, $5, $6, $7, 'ACTIVE', NOW())
	`, p.ID, p.RideCount, p.BonusRides, p.PriceRWF, input.CostPerRideRWF, p.ValidityDays, p.IsPromotional)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return p, nil
}

// UpdatePackage updates mutable package fields. Only non-nil fields are changed.
func (r *Repository) UpdatePackage(ctx context.Context, packageID string, input *UpdatePackageInput) (*Package, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Update ride_packages
	sets := []string{"updated_at = NOW()"}
	args := []interface{}{packageID}
	n := 2

	if input.Name != nil {
		sets = append(sets, "name = $"+itoa(n))
		args = append(args, *input.Name)
		n++
	}
	if input.RideCount != nil {
		sets = append(sets, "ride_count = $"+itoa(n))
		args = append(args, *input.RideCount)
		n++
	}
	if input.BonusRides != nil {
		sets = append(sets, "bonus_rides = $"+itoa(n))
		args = append(args, *input.BonusRides)
		n++
	}
	if input.ValidityDays != nil {
		sets = append(sets, "validity_days = $"+itoa(n))
		args = append(args, *input.ValidityDays)
		n++
	}
	if input.PriceRWF != nil {
		sets = append(sets, "price_rwf = $"+itoa(n))
		args = append(args, *input.PriceRWF)
		n++
	}
	if input.CostPerRideRWF != nil {
		sets = append(sets, "cost_per_ride_rwf = $"+itoa(n))
		args = append(args, *input.CostPerRideRWF)
		n++
	}
	if input.IsPromotional != nil {
		sets = append(sets, "is_promotional = $"+itoa(n))
		args = append(args, *input.IsPromotional)
		n++
	}

	query := "UPDATE ride_packages SET " + joinStrings(sets, ", ") +
		" WHERE id = $1 AND deleted_at IS NULL" +
		" RETURNING id, name, vehicle_type_id, ride_count, bonus_rides, validity_days, price_rwf, is_promotional, is_active, created_at, deleted_at"

	p := &Package{}
	var vehicleTypeID string
	err = tx.QueryRow(ctx, query, args...).Scan(
		&p.ID, &p.Name, &vehicleTypeID,
		&p.RideCount, &p.BonusRides, &p.ValidityDays, &p.PriceRWF,
		&p.IsPromotional, &p.IsActive, &p.CreatedAt, &p.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	// Fetch vehicle type code for returning
	err = tx.QueryRow(ctx, `SELECT code FROM vehicle_types WHERE id = $1`, vehicleTypeID).Scan(&p.VehicleTypeCode)
	if err != nil {
		return nil, err
	}

	// Update active version in ride_package_versions to keep in sync
	vsets := []string{}
	vargs := []interface{}{packageID}
	vn := 2

	if input.RideCount != nil {
		vsets = append(vsets, "rides = $"+itoa(vn))
		vargs = append(vargs, *input.RideCount)
		vn++
	}
	if input.BonusRides != nil {
		vsets = append(vsets, "bonus_rides = $"+itoa(vn))
		vargs = append(vargs, *input.BonusRides)
		vn++
	}
	if input.PriceRWF != nil {
		vsets = append(vsets, "price_rwf = $"+itoa(vn))
		vargs = append(vargs, *input.PriceRWF)
		vn++
	}
	if input.ValidityDays != nil {
		vsets = append(vsets, "validity_days = $"+itoa(vn))
		vargs = append(vargs, *input.ValidityDays)
		vn++
	}
	if input.CostPerRideRWF != nil {
		vsets = append(vsets, "cost_per_ride_rwf = $"+itoa(vn))
		vargs = append(vargs, *input.CostPerRideRWF)
		vn++
	}
	if input.IsPromotional != nil {
		vsets = append(vsets, "is_promotional = $"+itoa(vn))
		vargs = append(vargs, *input.IsPromotional)
		vn++
	}

	if len(vsets) > 0 {
		vquery := "UPDATE ride_package_versions SET " + joinStrings(vsets, ", ") + " WHERE package_id = $1 AND status = 'ACTIVE'"
		_, err = tx.Exec(ctx, vquery, vargs...)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return p, nil
}

// SetPackageActive toggles a package's active flag.
func (r *Repository) SetPackageActive(ctx context.Context, packageID string, active bool) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE ride_packages SET is_active = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`,
		packageID, active,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// DeletePackage soft-deletes a package by setting its deleted_at timestamp.
func (r *Repository) DeletePackage(ctx context.Context, packageID string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE ride_packages SET deleted_at = NOW(), is_active = FALSE, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`,
		packageID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func itoa(n int) string {
	return string(rune('0' + n)) // works for n < 10; enough for our field count
}

func joinStrings(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

// ListCatalog returns the v4 buyable catalog for a vehicle type: each active
// package's ACTIVE version with the best-matching active campaign applied.
// Campaign matching: status=ACTIVE, within its window, type GLOBAL/VEHICLE_TYPE/
// PACKAGE, targeting this package or its vehicle type (NULL target = any),
// highest priority wins. FIRST_PURCHASE/REFERRAL are resolved at purchase time.
func (r *Repository) ListCatalog(ctx context.Context, vehicleTypeCode string) ([]*CatalogPackage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT p.id, COALESCE(p.code, ''), p.name, vt.code,
		       v.id, v.version_number, v.rides, v.bonus_rides, v.price_rwf,
		       v.validity_days, v.is_promotional, v.is_unlimited,
		       c.id, c.code, c.override_price_rwf, c.override_rides, c.override_bonus_rides
		FROM ride_packages p
		JOIN vehicle_types vt ON vt.id = p.vehicle_type_id
		JOIN ride_package_versions v ON v.package_id = p.id AND v.status = 'ACTIVE'
		LEFT JOIN LATERAL (
			SELECT cc.id, cc.code, cc.override_price_rwf, cc.override_rides, cc.override_bonus_rides
			FROM campaigns cc
			WHERE cc.status = 'ACTIVE'
			  AND (cc.starts_at IS NULL OR cc.starts_at <= now())
			  AND (cc.ends_at   IS NULL OR cc.ends_at   >= now())
			  AND cc.type IN ('GLOBAL','VEHICLE_TYPE','PACKAGE')
			  AND (cc.target_vehicle_type_id IS NULL OR cc.target_vehicle_type_id = p.vehicle_type_id)
			  AND (cc.target_package_id      IS NULL OR cc.target_package_id      = p.id)
			ORDER BY cc.priority DESC, cc.created_at DESC
			LIMIT 1
		) c ON TRUE
		WHERE p.is_active = TRUE
		  AND p.deleted_at IS NULL
		  AND vt.code = $1
		ORDER BY v.price_rwf ASC
	`, vehicleTypeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CatalogPackage
	for rows.Next() {
		var (
			cp                        CatalogPackage
			ovPrice, ovRides, ovBonus *int
			campaignID, campaignCode  *string
		)
		if err := rows.Scan(
			&cp.ID, &cp.Code, &cp.Name, &cp.VehicleTypeCode,
			&cp.VersionID, &cp.VersionNumber, &cp.IncludedRides, &cp.BonusRides, &cp.NormalPriceRWF,
			&cp.ValidityDays, &cp.LaunchOffer, &cp.IsUnlimited,
			&campaignID, &campaignCode, &ovPrice, &ovRides, &ovBonus,
		); err != nil {
			return nil, err
		}
		// Apply campaign overrides (NULL = keep version value).
		cp.CurrentPriceRWF = cp.NormalPriceRWF
		if ovPrice != nil {
			cp.CurrentPriceRWF = *ovPrice
		}
		if ovRides != nil {
			cp.IncludedRides = *ovRides
		}
		if ovBonus != nil {
			cp.BonusRides = *ovBonus
		}
		if campaignID != nil {
			cp.CampaignID, cp.CampaignCode = campaignID, campaignCode
		}
		cp.TotalCredits = cp.IncludedRides + cp.BonusRides
		cp.IsPromotional = cp.LaunchOffer
		// Legacy mirrors for the pre-v4 mobile mapping.
		cp.PriceRWF = cp.CurrentPriceRWF
		cp.RideCount = cp.TotalCredits
		out = append(out, &cp)
	}
	return out, rows.Err()
}

// ListActiveCampaigns returns currently-running campaigns relevant to a vehicle
// type (GLOBAL or matching that type). Drivers see these as active promotions.
func (r *Repository) ListActiveCampaigns(ctx context.Context, vehicleTypeCode string) ([]*Campaign, error) {
	rows, err := r.db.Query(ctx, `
		SELECT c.id, c.code, c.name, c.type, c.starts_at, c.ends_at,
		       c.override_price_rwf, c.override_rides, c.override_bonus_rides
		FROM campaigns c
		LEFT JOIN vehicle_types vt ON vt.id = c.target_vehicle_type_id
		WHERE c.status = 'ACTIVE'
		  AND (c.starts_at IS NULL OR c.starts_at <= now())
		  AND (c.ends_at   IS NULL OR c.ends_at   >= now())
		  AND (c.target_vehicle_type_id IS NULL OR vt.code = $1)
		ORDER BY c.priority DESC, c.created_at DESC
	`, vehicleTypeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Campaign
	for rows.Next() {
		c := &Campaign{}
		if err := rows.Scan(
			&c.ID, &c.Code, &c.Name, &c.Type, &c.StartsAt, &c.EndsAt,
			&c.OverridePriceRWF, &c.OverrideRides, &c.OverrideBonusRides,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetPackageByID returns a single package by its ID.
func (r *Repository) GetPackageByID(ctx context.Context, packageID string) (*Package, error) {
	p := &Package{}
	err := r.db.QueryRow(ctx, `
		SELECT rp.id, rp.name, rp.vehicle_type_id, vt.code,
		       rp.ride_count, rp.bonus_rides, rp.validity_days, rp.price_rwf,
		       rp.is_promotional, rp.is_active, rp.created_at, rp.deleted_at
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE rp.id = $1
	`, packageID).Scan(
		&p.ID, &p.Name, &p.VehicleTypeID, &p.VehicleTypeCode,
		&p.RideCount, &p.BonusRides, &p.ValidityDays, &p.PriceRWF,
		&p.IsPromotional, &p.IsActive, &p.CreatedAt, &p.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// SumActiveCredits returns the total ride credits remaining across ALL of the
// driver's active, non-expired credit grants (a driver can hold several packages
// at once). This is the number to show as "credits left".
func (r *Repository) SumActiveCredits(ctx context.Context, driverUserID string) (int, error) {
	var total int
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(rides_remaining), 0)
		FROM driver_ride_credits
		WHERE driver_id = $1 AND is_active = TRUE AND rides_remaining > 0 AND expires_at > NOW()
	`, driverUserID).Scan(&total)
	return total, err
}

// GetActiveCredit returns the driver's best active credit (promos first, then earliest expiry).
// Returns ErrNotFound if the driver has no usable credits.
func (r *Repository) GetActiveCredit(ctx context.Context, driverUserID string) (*DriverCredit, error) {
	c := &DriverCredit{}
	err := r.db.QueryRow(ctx, `
		SELECT dc.id, dc.driver_id, dc.package_id, dc.vehicle_type_id, vt.code,
		       dc.rides_total, dc.rides_remaining, dc.is_promotional,
		       dc.expires_at, dc.is_active, dc.purchased_at
		FROM driver_ride_credits dc
		JOIN vehicle_types vt ON vt.id = dc.vehicle_type_id
		WHERE dc.driver_id = $1
		  AND dc.is_active = TRUE
		  AND dc.rides_remaining > 0
		  AND dc.expires_at > NOW()
		ORDER BY dc.is_promotional DESC, dc.expires_at ASC
		LIMIT 1
	`, driverUserID).Scan(
		&c.ID, &c.DriverID, &c.PackageID, &c.VehicleTypeID, &c.VehicleTypeCode,
		&c.RidesTotal, &c.RidesRemaining, &c.IsPromotional,
		&c.ExpiresAt, &c.IsActive, &c.PurchasedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// DeductCredit atomically decrements rides_remaining by 1 on the best usable credit.
// It is a no-op if the driver has no usable credit (ride already completed — caller
// should have gated at accept time).
func (r *Repository) DeductCredit(ctx context.Context, driverUserID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_ride_credits
		SET rides_remaining = rides_remaining - 1
		WHERE id = (
		    SELECT id
		    FROM driver_ride_credits
		    WHERE driver_id = $1
		      AND is_active = TRUE
		      AND rides_remaining > 0
		      AND expires_at > NOW()
		    ORDER BY is_promotional DESC, expires_at ASC
		    LIMIT 1
		)
	`, driverUserID)
	return err
}

// RefundCredit returns one ride to the driver's credit balance. It mirrors
// DeductCredit's row selection so the ride goes back onto the same credit it
// was most plausibly taken from, and never inflates a credit past its
// purchased total (rides_remaining < rides_total guard).
func (r *Repository) RefundCredit(ctx context.Context, driverUserID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_ride_credits
		SET rides_remaining = rides_remaining + 1
		WHERE id = (
		    SELECT id
		    FROM driver_ride_credits
		    WHERE driver_id = $1
		      AND is_active = TRUE
		      AND rides_remaining < rides_total
		      AND expires_at > NOW()
		    ORDER BY is_promotional DESC, expires_at ASC
		    LIMIT 1
		)
	`, driverUserID)
	return err
}

// PurchasePackage inserts a new credit record for a driver and returns it.
func (r *Repository) PurchasePackage(ctx context.Context, driverUserID, packageID, vehicleTypeID string, ridesTotal, validityDays int, isPromotional bool) (*DriverCredit, error) {
	c := &DriverCredit{}
	err := r.db.QueryRow(ctx, `
		WITH inserted AS (
		    INSERT INTO driver_ride_credits
		      (driver_id, package_id, vehicle_type_id, rides_total, rides_remaining, is_promotional, expires_at)
		    VALUES ($1, $2, $3, $4, $4, $5, NOW() + make_interval(days => $6))
		    RETURNING id, driver_id, package_id, vehicle_type_id, rides_total,
		              rides_remaining, is_promotional, expires_at, is_active, purchased_at
		)
		SELECT i.id, i.driver_id, i.package_id, i.vehicle_type_id, vt.code,
		       i.rides_total, i.rides_remaining, i.is_promotional,
		       i.expires_at, i.is_active, i.purchased_at
		FROM inserted i
		JOIN vehicle_types vt ON vt.id = i.vehicle_type_id
	`, driverUserID, packageID, vehicleTypeID, ridesTotal, isPromotional, validityDays).Scan(
		&c.ID, &c.DriverID, &c.PackageID, &c.VehicleTypeID, &c.VehicleTypeCode,
		&c.RidesTotal, &c.RidesRemaining, &c.IsPromotional,
		&c.ExpiresAt, &c.IsActive, &c.PurchasedAt,
	)
	return c, err
}

// ── Admin repo methods ────────────────────────────────────────────────────────

// GrantFreeTrialIfEligible grants the free-trial package for the driver's vehicle type.
// It is idempotent: if free_trial_used is already TRUE, it silently returns.
// Uses a transaction + SELECT FOR UPDATE to prevent duplicate grants under concurrency.
func (r *Repository) GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Lock the driver profile row to prevent race-condition double-grants.
	var alreadyUsed bool
	err = tx.QueryRow(ctx,
		`SELECT free_trial_used FROM driver_profiles WHERE user_id = $1 FOR UPDATE`,
		driverUserID,
	).Scan(&alreadyUsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // driver profile not found — skip silently
		}
		return err
	}
	if alreadyUsed {
		return nil // already received free trial
	}

	var pkgID, vehicleTypeID string
	var rideCount, bonusRides, validityDays int
	err = tx.QueryRow(ctx, `
		SELECT rp.id, rp.vehicle_type_id, rp.ride_count, rp.bonus_rides, rp.validity_days
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE vt.code = $1
		  AND rp.is_promotional = TRUE
		  AND rp.price_rwf = 0
		  AND rp.is_active = TRUE
		LIMIT 1
	`, vehicleTypeCode).Scan(&pkgID, &vehicleTypeID, &rideCount, &bonusRides, &validityDays)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no free trial package configured — skip silently
		}
		return err
	}

	totalCredits := rideCount + bonusRides
	_, err = tx.Exec(ctx, `
		INSERT INTO driver_ride_credits
		  (driver_id, package_id, vehicle_type_id, rides_total, rides_remaining, is_promotional, expires_at)
		VALUES ($1, $2, $3, $4, $4, TRUE, NOW() + make_interval(days => $5))
	`, driverUserID, pkgID, vehicleTypeID, totalCredits, validityDays)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		`UPDATE driver_profiles SET free_trial_used = TRUE WHERE user_id = $1`,
		driverUserID,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func packageCodeFromName(name string) string {
	code := strings.ToLower(strings.TrimSpace(name))
	code = strings.ReplaceAll(code, " ", "_")
	if len(code) > 40 {
		code = code[:40]
	}
	if code == "" {
		return "package"
	}
	return code
}
