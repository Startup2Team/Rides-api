package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/pkg/documents"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	"github.com/workspace/ride-platform/pkg/nationalid"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
	"github.com/workspace/ride-platform/pkg/timeutil"

	"github.com/workspace/ride-platform/internal/analytics"
)

// DriverPayoutRate is the share of the agreed fare the driver keeps. Our model
// is package-based (drivers buy ride credits and spend one at fare agreement) —
// there is NO per-ride commission, so the driver keeps 100% of the fare. This is
// the knob to change if a commission model is ever introduced.
const DriverPayoutRate = 1.0

// LocationUpdate is a single GPS update from the driver.
type LocationUpdate struct {
	Lat      float64
	Lng      float64
	SpeedKMH *float64
	Heading  *float64
}

// BatchLocationUpdate is a coordinate update within a batch send.
//
// No `required` on Lat/Lng: go-playground/validator treats it as "reject the
// zero value", which would reject lat==0 (the equator, which runs through
// Uganda) or lng==0 (prime meridian). min/max bounds are the real range check.
type BatchLocationUpdate struct {
	Lat       float64  `json:"lat"       validate:"min=-90,max=90"`
	Lng       float64  `json:"lng"       validate:"min=-180,max=180"`
	SpeedKMH  *float64 `json:"speed_kmh"`
	Heading   *float64 `json:"heading"`
	Timestamp int64    `json:"timestamp"`
}

// ApplyInput holds all fields for a driver application.
type ApplyInput struct {
	UserID                  string
	TransportType           string
	VehiclePlate            string
	LicenseNumber           string
	DateOfBirth             time.Time
	City                    string
	MomoPayCode             string
	MomoProvider            string
	Province                string
	District                string
	Sector                  string
	Cell                    string
	Village                 string
	Gender                  string
	PassengerSeats          *int
	LoadCapacityKg          *int
	LicenseExpiryDate       *time.Time
	InsuranceExpiryDate     *time.Time
	AuthorizationExpiryDate *time.Time
	// NationalIDNumber/NationalIDCountry are MANDATORY (DB-1 round 2: national
	// ID is now required for driver approval — see Apply and
	// admin.ApproveDriver's defensive gate). Apply rejects a missing value
	// before any DB write, then normalizes+validates (pkg/nationalid) before
	// this reaches the repository. The repository captures them onto users in
	// the same transaction as the driver_profiles write — first-write-wins for
	// a brand-new application; a REJECTED driver's resubmission overwrites
	// unconditionally (self-correction, see overwriteUserNationalIDTx).
	NationalIDNumber  string
	NationalIDCountry string
}

type CreditChecker interface {
	HasCredits(ctx context.Context, driverUserID, vehicleType string) (bool, error)
}

// ActiveRideCanceller lets ForceOffline driver-fault cancel an active ride
// before tearing down the driver's Redis state. Implemented by ride.Service and
// injected in main (driver → ride would be an import cycle).
type ActiveRideCanceller interface {
	CancelActiveRideForDriverExit(ctx context.Context, driverUserID, reason string) error
}

// WSNotifier lets UpdateLocation/UpdateLocationBatch push a driver's freshly
// persisted position straight to the customer watching their active ride.
// Implemented by *tracking.Hub's NotifyCustomer method (a thin wrapper around
// Hub.SendToCustomer using primitive types so this package doesn't have to
// import package tracking to declare the interface — tracking already imports
// driver for the WS handler's profile lookups, so the reverse import would
// cycle) and injected in main.
type WSNotifier interface {
	NotifyCustomer(rideID, msgType string, payload map[string]interface{})
}

// ArrivalMarker lets UpdateLocation/UpdateLocationBatch auto-transition a ride
// to DRIVER_ARRIVED when a driver's GPS ping lands inside the pickup geofence,
// without the driver ever tapping the manual "Arrived" button. Implemented by
// ride.Service.MarkDriverArrivedIfNear and injected in main (driver → ride
// would be an import cycle, same reason as ActiveRideCanceller above).
type ArrivalMarker interface {
	MarkDriverArrivedIfNear(ctx context.Context, rideID, driverProfileID string, point geo.Point) error
}

// Service handles driver business logic.
type Service struct {
	repo           *Repository
	redis          goredis.UniversalClient
	analytics      *analytics.Service
	cfg            *config.Config
	log            zerolog.Logger
	wsNotifier     WSNotifier
	creditChecker  CreditChecker
	expiryNotifier expiryNotifier
	rideCanceller  ActiveRideCanceller
	arrivalMarker  ArrivalMarker
}

func NewService(repo *Repository, rdb goredis.UniversalClient, ana *analytics.Service, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{repo: repo, redis: rdb, analytics: ana, cfg: cfg, log: log}
}

func (s *Service) SetCreditChecker(cc CreditChecker) {
	s.creditChecker = cc
}

// SetActiveRideCanceller wires the ride service so logout can't strand (or
// silently escape) an agreed ride — see ForceOffline.
func (s *Service) SetActiveRideCanceller(c ActiveRideCanceller) {
	s.rideCanceller = c
}

// SetWSNotifier wires the WebSocket hub so location updates can relay to the
// customer in real time — see relayLocationToCustomer.
func (s *Service) SetWSNotifier(n WSNotifier) {
	s.wsNotifier = n
}

// SetArrivalMarker wires the ride service so a location update can auto-fire
// the DRIVER_ARRIVED transition when the driver reaches the pickup geofence —
// see maybeAutoMarkArrived.
func (s *Service) SetArrivalMarker(m ArrivalMarker) {
	s.arrivalMarker = m
}

// DemandHeatmap returns bucketed pickup demand over the last windowMin minutes,
// optionally scoped to radiusM metres around center — so a driver can see where
// riders are requesting and reposition. Read-only.
func (s *Service) DemandHeatmap(ctx context.Context, windowMin int, center *geo.Point, radiusM int) ([]DemandCell, error) {
	cells, err := s.repo.DemandHeatmap(ctx, windowMin, center, radiusM)
	if err != nil {
		s.log.Error().Err(err).Int("window_min", windowMin).Msg("driver demand-heatmap: query failed")
		return nil, err
	}
	s.log.Debug().Int("window_min", windowMin).Int("cells", len(cells)).Msg("driver demand-heatmap served")
	return cells, nil
}

// resolveNationalIDInput normalizes and format-validates a national ID
// against the platform's NATIONAL_ID_REQUIRED rollout flag
// (config.DriverConfig.NationalIDRequired, DB-1 staged rollout).
//
// Capture/validation stays active whenever BOTH fields are present,
// regardless of the flag — a bad format must never reach Postgres just to be
// caught by the lenient backstop CHECK there (migration 080/081). Only
// whether a value must be present AT ALL is gated: with required=false, a
// missing/partial pair is treated as "not supplied" (both returned empty, no
// error) so old app versions that don't yet send these fields keep applying
// exactly as before; with required=true this is byte-for-byte the original
// DB-1 round 2 behaviour.
func resolveNationalIDInput(required bool, country, number string) (normCountry, normNumber string, err error) {
	if country == "" || number == "" {
		if required {
			return "", "", apperrors.New(http.StatusBadRequest, "NATIONAL_ID_REQUIRED",
				"national_id_number and national_id_country are required")
		}
		return "", "", nil
	}
	normCountry, normNumber = nationalid.Normalize(country, number)
	if verr := nationalid.Validate(normCountry, normNumber); verr != nil {
		return "", "", apperrors.New(http.StatusBadRequest, "INVALID_NATIONAL_ID", verr.Error())
	}
	return normCountry, normNumber, nil
}

// Apply submits a driver application.
// In dev mode (DEV_AUTO_APPROVE_DRIVERS=true) the profile is immediately
// approved and the user's role_state is promoted to DRIVER_ACTIVE so
// they can go online without waiting for an admin action.
func (s *Service) Apply(ctx context.Context, in ApplyInput) (*Profile, error) {
	// National ID: mandatory only when cfg.Driver.NationalIDRequired is set
	// (DB-1 staged rollout — see resolveNationalIDInput). Reject a missing
	// value before any DB write when required, then normalize + format-
	// validate (pkg/nationalid) whenever a value IS supplied. This applies to
	// every call through Apply, including a REJECTED driver's resubmission: a
	// resubmission missing the field when the flag is on is not "keep the old
	// value", it is a bad request, same as any other required field.
	country, number, err := resolveNationalIDInput(s.cfg.Driver.NationalIDRequired, in.NationalIDCountry, in.NationalIDNumber)
	if err != nil {
		return nil, err
	}
	in.NationalIDCountry, in.NationalIDNumber = country, number

	existing, err := s.repo.FindProfileByUserID(ctx, in.UserID)
	if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
		return nil, err
	}
	if err == nil && existing != nil {
		// Profile already exists.
		if existing.ApprovalStatus != StatusApproved && existing.ApprovalStatus != StatusSuspended {
			// Profile exists and is not yet approved or suspended (PENDING,
			// PENDING_REVIEW, REJECTED, or NEEDS_MORE_INFO); allow updating details
			// and resubmitting via /apply.
			if rerr := s.repo.UpdateProfileForResubmission(ctx, in); rerr != nil {
				return nil, mapApplyErr(rerr)
			}

			if s.cfg.Driver.DevAutoApprove {
				if aerr := s.repo.SetApprovalStatus(ctx, existing.ID, StatusApproved, "", nil); aerr != nil {
					return nil, fmt.Errorf("dev auto-approve: %w", aerr)
				}
				if aerr := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_ACTIVE"); aerr != nil {
					return nil, fmt.Errorf("update role state: %w", aerr)
				}
				// Keep the dev shortcut a full shortcut: without this the driver's
				// vehicle stays PENDING_REVIEW (per-vehicle approval, migration 089)
				// even though DEV_AUTO_APPROVE_DRIVERS was explicitly meant to bypass
				// review entirely, and they'd immediately hit VEHICLE_NOT_APPROVED on
				// their first go-online. Best-effort/logged: it must not fail an
				// otherwise-successful dev approval.
				if verr := s.repo.SetActiveVehicleApprovalStatus(ctx, existing.ID, VehicleStatusApproved); verr != nil {
					s.log.Warn().Err(verr).Str("driver_profile_id", existing.ID).
						Msg("DEV_AUTO_APPROVE_DRIVERS: could not sync active vehicle approval status")
				}
				s.log.Warn().Str("user_id", in.UserID).Msg("DEV_AUTO_APPROVE_DRIVERS: resubmitted driver approved instantly")
			} else {
				if aerr := s.reopenForReview(ctx, existing); aerr != nil {
					return nil, fmt.Errorf("reopen review: %w", aerr)
				}
			}
			return s.repo.FindProfileByUserID(ctx, in.UserID)
		}

		if existing.ApprovalStatus == StatusApproved {
			// Driver is already approved; adding an additional vehicle (e.g. Hilux, Motor, Cab).
			// Add the new vehicle to their existing profile.
			if vErr := s.repo.CreateVehicleFromApply(ctx, existing.ID, in); vErr != nil {
				if isUniqueViolation(vErr) {
					return nil, mapApplyErr(vErr)
				}
				s.log.Warn().Err(vErr).Str("profile_id", existing.ID).Msg("create additional vehicle from apply failed")
			}
			if aerr := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_ACTIVE"); aerr != nil {
				return nil, fmt.Errorf("update role state: %w", aerr)
			}
			if verr := s.repo.SetActiveVehicleApprovalStatus(ctx, existing.ID, VehicleStatusApproved); verr != nil {
				s.log.Warn().Err(verr).Str("driver_profile_id", existing.ID).
					Msg("DEV_AUTO_APPROVE_DRIVERS: could not sync active vehicle approval status")
			}
			existing.ApprovalStatus = "APPROVED"
			s.log.Warn().Str("user_id", in.UserID).Msg("DEV_AUTO_APPROVE_DRIVERS: existing pending profile approved")
			return existing, nil
		}

		return nil, apperrors.ErrDriverAlreadyApplied
	}

	profile, err := s.repo.CreateProfile(ctx, in)
	if err != nil {
		if isUniqueViolation(err) {
			if existingProfile, findErr := s.repo.FindProfileByUserID(ctx, in.UserID); findErr == nil && existingProfile != nil {
				if rerr := s.repo.UpdateProfileForResubmission(ctx, in); rerr == nil {
					_ = s.reopenForReview(ctx, existingProfile)
					return s.repo.FindProfileByUserID(ctx, in.UserID)
				}
			}
		}
		return nil, mapApplyErr(err)
	}
	// Mirror the application's vehicle into driver_vehicles (the multi-vehicle
	// source of truth). Tolerate a duplicate plate: the profile row is already
	// created and the vehicles list lazily backfills from it.
	if vErr := s.repo.CreateVehicleFromApply(ctx, profile.ID, in); vErr != nil && !isUniqueViolation(vErr) {
		s.log.Warn().Err(vErr).Str("profile_id", profile.ID).Msg("create vehicle from apply failed (non-fatal)")
	}

	if s.cfg.Driver.DevAutoApprove {
		// Skip admin queue — approve immediately for dev/testing.
		if err := s.repo.SetApprovalStatus(ctx, profile.ID, "APPROVED", "", nil); err != nil {
			return nil, fmt.Errorf("dev auto-approve: %w", err)
		}
		if err := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_ACTIVE"); err != nil {
			return nil, fmt.Errorf("update role state: %w", err)
		}
		if verr := s.repo.SetActiveVehicleApprovalStatus(ctx, profile.ID, VehicleStatusApproved); verr != nil {
			s.log.Warn().Err(verr).Str("driver_profile_id", profile.ID).
				Msg("DEV_AUTO_APPROVE_DRIVERS: could not sync active vehicle approval status")
		}
		profile.ApprovalStatus = "APPROVED"
		s.log.Warn().Str("user_id", in.UserID).Msg("DEV_AUTO_APPROVE_DRIVERS: driver approved instantly — disable in production")
	} else {
		if err := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_PENDING"); err != nil {
			s.log.Warn().Err(err).Str("user_id", in.UserID).Msg("update role state failed (non-fatal)")
		}
		// Confirm receipt so the applicant knows their submission landed and is in
		// the review queue (in-app + push to every registered device).
		if s.expiryNotifier != nil {
			go func(uid string) {
				defer func() { _ = recover() }()
				s.expiryNotifier.SendToAllDevices(context.Background(), uid, "Application received",
					"We've received your driver application. We'll review your documents and let you know as soon as it's approved.",
					"driver", map[string]string{"type": "driver_application_received"})
			}(in.UserID)
		}
	}

	return profile, nil
}

// editableNationalIDStatuses whitelists the approval_status values a driver
// may still self-correct their OWN national ID under. Anything else —
// APPROVED, SUSPENDED, or a future status this list hasn't been taught about
// — is locked; the whitelist (not a blacklist of the two locked ones) is
// deliberate so a new status defaults to closed, not open.
var editableNationalIDStatuses = map[string]bool{
	"PENDING_REVIEW":  true,
	"REJECTED":        true,
	"NEEDS_MORE_INFO": true,
}

// editableNationalIDStatusList is editableNationalIDStatuses as a slice, for
// binding into the `= ANY($n)` clause in Repository.SetOwnNationalID. Derived
// from the map (not re-typed) so the atomic DB guard and this whitelist can
// never drift apart.
var editableNationalIDStatusList = func() []string {
	statuses := make([]string, 0, len(editableNationalIDStatuses))
	for status := range editableNationalIDStatuses {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	return statuses
}()

// ErrNationalIDLocked is returned when a driver tries to self-correct their
// national ID after approval. Approval is a decision about a specific,
// physically-checked document; once made, the only remaining way to correct
// the number on file is an admin edit (internal/admin.SetDriverNationalID),
// which is audited.
var ErrNationalIDLocked = apperrors.New(http.StatusConflict, "NATIONAL_ID_LOCKED",
	"Your national ID is locked after approval. Contact support to correct it.")

// SetOwnNationalID lets a driver correct their OWN national ID while their
// approval is not yet final (see editableNationalIDStatuses). This is the
// owner self-correction path (DB-1 round 2) — it replaces the old
// mask-own-view + silent-resubmit behaviour: the driver can now see their
// full number (FindProfileByUserID) AND fix it here, right up until an admin
// approves them, at which point it locks.
//
// The existence check (FindProfileByUserID, for a 404 when there is somehow
// no driver_profiles row) and the approval-status gate are deliberately NOT
// the same read: the gate is enforced by repo.SetOwnNationalID itself, in the
// SAME statement as the write (WHERE dp.approval_status = ANY(editable...)),
// so there is no window between "check" and "write" for an admin's
// concurrent ApproveDriver to land in. A stale profile.ApprovalStatus read
// here is never used to authorize the write — that closes the TOCTOU where a
// driver could race their own edit against an in-flight approval and change
// the number on file after APPROVED.
func (s *Service) SetOwnNationalID(ctx context.Context, userID, country, number string) error {
	if _, err := s.repo.FindProfileByUserID(ctx, userID); err != nil {
		return err
	}

	normCountry, normNumber := nationalid.Normalize(country, number)
	if verr := nationalid.Validate(normCountry, normNumber); verr != nil {
		return apperrors.New(http.StatusBadRequest, "INVALID_NATIONAL_ID", verr.Error())
	}

	if err := s.repo.SetOwnNationalID(ctx, userID, normCountry, normNumber); err != nil {
		if errors.Is(err, ErrNationalIDTaken) {
			return apperrors.New(http.StatusConflict, "NATIONAL_ID_ALREADY_REGISTERED",
				"This national ID is already registered to another account.")
		}
		return err
	}
	return nil
}

// UpdateProfile updates mutable driver profile fields.
func (s *Service) UpdateProfile(ctx context.Context, userID string, city, momoPayCode, momoProvider, gender, fcmToken *string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.UpdateProfileFields(ctx, profile.ID, city, momoPayCode, momoProvider, gender, fcmToken)
}

// AcceptPolicy marks the driver policy as accepted.
func (s *Service) AcceptPolicy(ctx context.Context, userID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.SetPolicyAccepted(ctx, profile.ID)
}

// Driver approval-status values — the CANONICAL contract shared with the
// mobile app and admin web. These are long-lived Postgres values AND wire
// values both clients match on literally; never rename or repurpose one.
//
// A single hardcoded "PENDING" (missing the "_REVIEW" suffix) in
// UploadDocument used to silently desync a resubmitting driver's Go-level
// status from what admin's queue query (WHERE approval_status =
// 'PENDING_REVIEW') was looking for — the driver's profile said "PENDING"
// forever and never resurfaced for review. These constants, plus
// reopenForReview below being the one place that performs the transition,
// are what close that drift for good.
const (
	StatusPendingReview = "PENDING_REVIEW"
	StatusApproved      = "APPROVED"
	StatusRejected      = "REJECTED"
	StatusNeedsMoreInfo = "NEEDS_MORE_INFO"
	StatusSuspended     = "SUSPENDED"
)

// resubmissionStatuses lists which approval_status values a fresh document
// upload or re-/apply must reopen into review from: an APPROVED driver
// replacing their papers (the approval no longer describes what's on file),
// and a REJECTED/NEEDS_MORE_INFO driver correcting theirs (the entire point
// of telling them why). Anything else — PENDING_REVIEW itself, or
// SUSPENDED — is deliberately excluded: SUSPENDED in particular must never
// be silently reopened by a document upload, only an admin
// (admin.ReinstateDriver) lifts a suspension.
var resubmissionStatuses = map[string]bool{
	StatusApproved:      true,
	StatusRejected:      true,
	StatusNeedsMoreInfo: true,
}

// vehicleResubmissionStatuses is resubmissionStatuses' per-vehicle
// counterpart (migration 089): which driver_vehicles.approval_status values
// a fresh document upload for a NON-ACTIVE vehicle must reopen into review
// from. No NEEDS_MORE_INFO or SUSPENDED at the vehicle level (see the
// VehicleStatus* constants in vehicles.go) — just APPROVED (papers replaced,
// the approval no longer describes what's on file) and REJECTED (the driver
// correcting what they were told was wrong). PENDING_REVIEW is deliberately
// excluded: it is already where this vehicle needs to be, so re-writing it
// would only be a needless UPDATE + updated_at bump on every upload.
var vehicleResubmissionStatuses = map[string]bool{
	VehicleStatusApproved: true,
	VehicleStatusRejected: true,
}

// reopenForReview transitions a driver profile back to PENDING_REVIEW after a
// resubmission (new document upload, or a re-/apply from REJECTED /
// NEEDS_MORE_INFO) and mirrors role_state to DRIVER_PENDING so the driver
// can't stay online on an approval that no longer applies. It clears any
// stale rejection_reason (SetApprovalStatus's rejectionReason arg) since the
// driver has just acted on it.
//
// This is the ONE call site that performs the approval_status transition —
// UploadDocument's two resubmission paths and Apply's resubmission branch all
// go through it, so the status string used to reopen review can never drift
// between them again (see the const block's doc comment above).
//
// The approval_status write is the one that must not silently fail — a
// resubmission that stores the new document/fields but never reopens review
// strands the driver outside the admin queue forever, which is the exact bug
// this fixes. The role_state mirror is best-effort and logged: it only
// affects whether SetAvailability's stale role_state blocks the driver from
// seeing "you're pending", not whether an admin can act on them.
//
// profile.ApprovalStatus must be the status BEFORE this call (the caller's
// already-fetched profile, not a re-read) — it decides whether the
// force-offline eviction below applies.
func (s *Service) reopenForReview(ctx context.Context, profile *Profile) error {
	if err := s.repo.SetApprovalStatus(ctx, profile.ID, StatusPendingReview, "", nil); err != nil {
		return fmt.Errorf("set approval status to %s: %w", StatusPendingReview, err)
	}
	if err := s.repo.UpdateUserRoleState(ctx, profile.UserID, "DRIVER_PENDING"); err != nil {
		s.log.Error().Err(err).
			Str("driver_profile_id", profile.ID).
			Str("user_id", profile.UserID).
			Msg("driver: reopened review but could not mirror role_state to DRIVER_PENDING")
	}

	// An APPROVED driver reopening review may currently be online: the
	// approval that let them go online no longer describes what's on file
	// (that's the whole point of reopening), but this transition only gates
	// FUTURE go-online calls (SetAvailability requires APPROVED) — it does
	// nothing about a session that is already online. Left alone that
	// driver keeps is_online=TRUE, a Redis DriverState, and a pin in the
	// Redis geo index the customer nearby-map reads from, even though the
	// matching engine's dispatch path is approval-gated and will never send
	// them an offer. That is exactly the Redis-vs-Postgres drift the
	// platform must never have (a ghost pin with no way to ever get a
	// ride). REJECTED and NEEDS_MORE_INFO can never have gotten online in
	// the first place (SetAvailability requires APPROVED), so this only
	// ever fires coming from APPROVED. Mirrors SuspendDriver's eviction
	// (internal/admin/drivers.go) and is best-effort/logged like the
	// role_state mirror above — a Redis hiccup must not fail the
	// resubmission the driver is actively trying to complete.
	if profile.ApprovalStatus == StatusApproved {
		if err := s.repo.UpdateOnlineStatus(ctx, profile.UserID, false); err != nil {
			s.log.Error().Err(err).
				Str("driver_profile_id", profile.ID).
				Str("user_id", profile.UserID).
				Msg("driver: reopened review from APPROVED but could not force is_online=false")
		}
		s.evictOnlineDriverFromRedis(ctx, profile.ID, profile.TransportType)
	}
	return nil
}

// evictOnlineDriverFromRedis clears an online driver's Redis presence
// (DriverState + geo index ZRem) — the Redis half of reopenForReview's
// force-offline eviction from APPROVED. Split out from reopenForReview so
// the Redis-only behavior can be unit-tested against miniredis without a
// Postgres dependency (see reopenForReview's Postgres writes, which cannot).
//
// Best-effort/logged, mirroring SuspendDriver's eviction (internal/admin/
// drivers.go): a Redis hiccup must not fail the resubmission the driver is
// actively trying to complete. s.redis is nil in some test setups that never
// wire a Redis client — guarded like SuspendDriver's `if s.rdb != nil`.
func (s *Service) evictOnlineDriverFromRedis(ctx context.Context, profileID, transportType string) {
	if s.redis == nil {
		return
	}
	if err := s.redis.Set(ctx, rkeys.K.DriverState(profileID), "OFFLINE", 0).Err(); err != nil {
		s.log.Error().Err(err).
			Str("driver_profile_id", profileID).
			Msg("driver: reopened review from APPROVED but could not set Redis driver state to OFFLINE")
	}
	if err := s.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(transportType), profileID).Err(); err != nil {
		s.log.Error().Err(err).
			Str("driver_profile_id", profileID).
			Msg("driver: reopened review from APPROVED but could not evict from Redis geo index")
	}
}

// UploadDocument records a new version of a driver document (URL only — file
// hosting is external). sha256 may be empty when the client uploaded via a
// presigned URL and did not report a digest.
//
// If the driver was APPROVED, REJECTED, or NEEDS_MORE_INFO, uploading a new
// document sends them back to PENDING_REVIEW (reopenForReview). Approval is a
// statement about specific papers; swapping those papers invalidates it. A
// REJECTED/NEEDS_MORE_INFO driver resubmitting is the whole point of showing
// them the reason — without this they could re-upload forever and never
// resurface in the admin queue. It is enforced here, server-side, because
// hiding the button in the app is not enforcement.
// UploadDocument records one KYC document for the calling driver.
//
// vehicleID must be supplied for vehicle-level types and omitted for
// person-level ones. Both directions are enforced: a mismatch is a 400 here
// rather than a constraint violation surfacing as a 500, and the vehicle must
// belong to this driver — without that check a driver could attach paperwork to
// someone else's vehicle and push a stranger's account back into review.
func (s *Service) UploadDocument(ctx context.Context, userID, documentType, fileURL, sha256 string, vehicleID *string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	documentType = documents.Normalize(documentType)
	if !documents.IsValid(documentType) {
		return apperrors.New(http.StatusBadRequest, "UNSUPPORTED_DOCUMENT_TYPE",
			"unsupported document type: "+documentType)
	}

	needsVehicle := documents.RequiresVehicle(documentType)
	if needsVehicle && (vehicleID == nil || *vehicleID == "") {
		pVehID, pErr := s.repo.GetPrimaryVehicleID(ctx, profile.ID)
		if pErr == nil && pVehID != nil && *pVehID != "" {
			vehicleID = pVehID
		}
	}
	switch {
	case needsVehicle && (vehicleID == nil || *vehicleID == ""):
		return apperrors.New(http.StatusBadRequest, "VEHICLE_REQUIRED",
			documentType+" belongs to a specific vehicle — vehicle_id is required")
	case !needsVehicle && vehicleID != nil && *vehicleID != "":
		return apperrors.New(http.StatusBadRequest, "VEHICLE_NOT_APPLICABLE",
			documentType+" describes the driver, not a vehicle — omit vehicle_id")
	}

	// targetVehicle is fetched (not just an ownership boolean) because the
	// scoping decision below needs its IsActive and ApprovalStatus, not just
	// whether the driver owns it.
	var targetVehicle *Vehicle
	if needsVehicle {
		v, vErr := s.repo.GetVehicle(ctx, profile.ID, *vehicleID)
		if vErr != nil {
			if errors.Is(vErr, apperrors.ErrNotFound) {
				// 404 rather than 403: the caller should not learn whether a vehicle id
				// they do not own exists.
				return apperrors.New(http.StatusNotFound, "VEHICLE_NOT_FOUND", "vehicle not found")
			}
			return vErr
		}
		targetVehicle = v
	} else {
		vehicleID = nil // normalise "" to NULL so the CHECK constraint holds
	}

	if err := s.repo.UpsertDocument(ctx, profile.ID, documentType, fileURL, sha256, vehicleID, false); err != nil {
		return err
	}

	// A document for a specific, NON-ACTIVE vehicle reopens review for ONLY
	// that vehicle: the driver may be actively earning on a DIFFERENT vehicle
	// right now, and must not be pulled offline or have their whole
	// driver_profiles.approval_status reopened just because they uploaded
	// paperwork for vehicle #2 while working on vehicle #1 — that used to
	// force-evict an APPROVED, currently-working driver over paperwork for a
	// vehicle they are not even driving.
	//
	// Person-level documents, and documents for the vehicle the driver is
	// CURRENTLY working on, fall through unchanged to the whole-driver
	// behaviour below: uploading new papers for the vehicle actually in use,
	// or for the driver themselves, still needs to pull them back into
	// review — that vehicle's own approval_status is deliberately left alone
	// here in either case; the whole-driver reopen (profile back to
	// PENDING_REVIEW, forced offline) is what gates them, same as before this
	// feature existed.
	if needsVehicle && !targetVehicle.IsActive {
		if vehicleResubmissionStatuses[targetVehicle.ApprovalStatus] {
			if err := s.repo.SetVehicleApprovalStatus(ctx, targetVehicle.ID, VehicleStatusPendingReview, nil); err != nil {
				// Same trade-off as the whole-driver path below: the document is
				// already stored, so failing the request now would misreport a
				// successful upload as failed. Log loudly — a vehicle stuck
				// un-reopened on unreviewed papers needs to be visible.
				s.log.Error().Err(err).
					Str("driver_profile_id", profile.ID).
					Str("vehicle_id", targetVehicle.ID).
					Str("document_type", documentType).
					Msg("documents: uploaded for a non-active vehicle but could not reopen that vehicle's review")
				return nil
			}
			s.log.Info().
				Str("driver_profile_id", profile.ID).
				Str("vehicle_id", targetVehicle.ID).
				Str("document_type", documentType).
				Msg("documents: driver replaced a non-active vehicle's document — that vehicle's review reopened; driver profile and online status untouched")
		}
		return nil
	}

	if resubmissionStatuses[profile.ApprovalStatus] {
		fromStatus := profile.ApprovalStatus
		if err := s.reopenForReview(ctx, profile); err != nil {
			// The document is already stored; failing the request now would tell
			// the driver the upload failed when it did not. Log loudly instead —
			// a driver left un-reopened on unreviewed papers needs to be visible.
			s.log.Error().Err(err).
				Str("driver_profile_id", profile.ID).
				Str("document_type", documentType).
				Str("from_status", fromStatus).
				Msg("documents: uploaded but could not reopen review — driver is stuck on unreviewed papers")
			return nil
		}
		if fromStatus == StatusApproved {
			s.log.Warn().
				Str("driver_profile_id", profile.ID).
				Str("document_type", documentType).
				Msg("documents: approved driver replaced a document — review reopened")
		} else {
			s.log.Info().
				Str("driver_profile_id", profile.ID).
				Str("document_type", documentType).
				Str("from_status", fromStatus).
				Msg("documents: driver resubmitted a document — review reopened")
		}
	}
	return nil
}

// ListDocuments returns all uploaded documents for a driver.
func (s *Service) ListDocuments(ctx context.Context, userID string) ([]*Document, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListDocuments(ctx, profile.ID)
}

// ForceOffline sets a driver OFFLINE unconditionally, ignoring any active-ride
// guard and cooldown. Used during logout so the driver is always cleanly removed
// from the matching pool even if their Redis state is stale.
//
// It no longer skips the ride, though: before this teardown, any active ride in
// CONFIRMED..IN_PROGRESS is driver-fault cancelled (penalty ladder, forfeited
// credit, customer notified). Unconditionally deleting driver:<id>:active_ride
// made Logout a one-tap penalty-free escape from an agreed ride. Cancel errors
// are logged but never block the logout itself.
func (s *Service) ForceOffline(ctx context.Context, userID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		// Not a driver — nothing to do.
		return nil
	}
	if s.rideCanceller != nil {
		if err := s.rideCanceller.CancelActiveRideForDriverExit(ctx, userID, "driver logged out during an active ride"); err != nil {
			s.log.Error().Err(err).Str("driver_id", profile.ID).
				Msg("driver: could not cancel active ride on force-offline — going offline anyway")
		}
	}
	if err := s.redis.Del(ctx, rkeys.K.DriverActiveRide(profile.ID)).Err(); err != nil {
		return err
	}
	if err := s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "OFFLINE", 0).Err(); err != nil {
		return err
	}
	if err := s.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), profile.ID).Err(); err != nil {
		return err
	}
	if err := s.repo.UpdateOnlineStatus(ctx, userID, false); err != nil {
		return err
	}
	s.log.Info().Str("driver_id", profile.ID).Msg("driver: force-offlined on logout")
	return nil
}

// SetAvailability toggles a driver online/offline with cooldown enforcement.
func (s *Service) SetAvailability(ctx context.Context, userID string, isOnline bool) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if isOnline {
		if profile.ApprovalStatus != "APPROVED" {
			return apperrors.ErrDriverNotActive
		}

		// 0. Active vehicle must itself be APPROVED (migration 089, per-vehicle
		// approval). A driver stays APPROVED while a newly added second vehicle
		// awaits review, but must not be able to switch to and drive THAT
		// vehicle before it clears review — this is the go-online-time half of
		// the same gate ActivateVehicle enforces at switch-time; SetAvailability
		// needs its own copy because a driver can already be online with a
		// vehicle active when it is (re)opened for review (UploadDocument ->
		// reopenForReview's per-vehicle scoping) and then goes offline/online
		// again without switching vehicles at all.
		activeVehicleStatus, vErr := s.repo.GetActiveVehicleApprovalStatus(ctx, profile.ID)
		if vErr != nil {
			return vErr
		}
		if activeVehicleStatus != "" && activeVehicleStatus != VehicleStatusApproved {
			return apperrors.New(http.StatusConflict, "VEHICLE_NOT_APPROVED",
				"Your active vehicle is awaiting approval. Switch to an approved vehicle or wait for review.")
		}

		// 1. License Expiry Check
		if profile.LicenseExpiryDate != nil && profile.LicenseExpiryDate.Before(time.Now()) {
			return apperrors.New(http.StatusBadRequest, "EXPIRED_LICENSE", "Your driver license has expired. Update your driver license documents to continue.")
		}

		// 2. Insurance Expiry Check
		if profile.InsuranceExpiryDate != nil && profile.InsuranceExpiryDate.Before(time.Now()) {
			return apperrors.New(http.StatusBadRequest, "EXPIRED_INSURANCE", "Your vehicle insurance has expired. Update your insurance documents to continue.")
		}

		// 3. Authorization / Permit Expiry Check
		if profile.AuthorizationExpiryDate != nil && profile.AuthorizationExpiryDate.Before(time.Now()) {
			return apperrors.New(http.StatusBadRequest, "EXPIRED_AUTHORIZATION", "Your vehicle authorization permit has expired. Update your authorization documents to continue.")
		}

		// 4. Active Package / Credits Check
		if s.creditChecker != nil {
			hasCredits, err := s.creditChecker.HasCredits(ctx, userID, profile.TransportType)
			if err != nil {
				return err
			}
			if !hasCredits {
				// Surface the block as an in-app + push notification so the driver
				// sees it even after they navigate away from the go-online error.
				if s.expiryNotifier != nil {
					s.expiryNotifier.SendToAllDevices(ctx, userID, "Buy a package to keep riding",
						"You're out of ride credits. Purchase a package to go online and accept rides.",
						"driver", map[string]string{"type": "credits_low"})
				}
				return apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding.")
			}
		}
		offlineKey := rkeys.K.DriverOfflineAt(profile.ID)
		_, redisErr := s.redis.Get(ctx, offlineKey).Result()
		if redisErr == nil {
			s.log.Info().Str("driver_id", profile.ID).Msg("driver came online within cooldown — penalties preserved")
		}
		// Always clear stale location history and reset the plausibility window,
		// even on an app-restart session-restore (driverProfile.isOnline=true path).
		// Without this the first HTTP location update after restart compares against
		// an old session's position and gets rejected as GPS_PLAUSIBILITY.
		s.redis.Del(ctx, rkeys.K.DriverLocationHistory(profile.ID))
		s.redis.Del(ctx, rkeys.K.GPSAnomalyCount(profile.ID))
		s.redis.Set(ctx, rkeys.K.DriverGracePeriod(profile.ID), "1", 60*time.Second)

		// If the driver has an active ride (e.g. app restarted mid-trip), preserve
		// the ON_TRIP Redis state so the matching engine doesn't re-pool them.
		// We still refresh the grace period above so location updates don't fail.
		activeRide, _ := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profile.ID)).Result()
		if activeRide == "" {
			// No active ride — full online transition.
			s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "AVAILABLE", 0)
			s.analytics.Publish(ctx, "driver.went_online", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
		}
	} else {
		// Verify no active ride before going offline.
		// Cross-check Redis against the DB: if the Redis key is stale (ride already
		// completed/cancelled) we clean it up and allow the offline transition.
		// This prevents the driver from being permanently locked offline when
		// a CompleteRide Redis write failed silently after the DB write succeeded.
		activeRide, _ := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profile.ID)).Result()
		if activeRide != "" {
			// Redis says active — verify the ride is actually still open in the DB.
			hasActiveInDB := s.repo.HasActiveRide(ctx, userID)
			if hasActiveInDB {
				return apperrors.New(409, "ACTIVE_RIDE", "complete your active ride before going offline")
			}
			// Stale Redis key — ride is done in DB. Clean up and continue.
			s.redis.Del(ctx, rkeys.K.DriverActiveRide(profile.ID))
			s.log.Warn().Str("driver_id", profile.ID).Str("stale_ride_id", activeRide).Msg("driver: cleaned up stale active_ride Redis key on offline transition")
		}
		offlineKey := rkeys.K.DriverOfflineAt(profile.ID)
		s.redis.Set(ctx, offlineKey, time.Now().UTC().Format(time.RFC3339),
			time.Duration(s.cfg.Driver.OfflineCooldownMinutes)*time.Minute)
		s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "OFFLINE", 0)
		s.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), profile.ID)
		s.analytics.Publish(ctx, "driver.went_offline", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
	}

	return s.repo.UpdateOnlineStatus(ctx, userID, isOnline)
}

// UpdateLocation processes a GPS update: plausibility check, Redis write, DB write (async).
func (s *Service) UpdateLocation(ctx context.Context, userID string, update LocationUpdate) error {
	newPoint := geo.Point{Lat: update.Lat, Lng: update.Lng}
	if err := newPoint.Validate(); err != nil {
		return err
	}

	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if !profile.IsOnline || profile.ApprovalStatus != "APPROVED" {
		return apperrors.ErrDriverNotActive
	}

	if anomaly, speed := s.checkGPSPlausibility(ctx, profile.ID, newPoint); anomaly {
		_ = s.repo.LogGPSAnomaly(ctx, profile.ID, speed, nil, &newPoint)

		anomalyKey := rkeys.K.GPSAnomalyCount(profile.ID)
		count, _ := s.redis.Incr(ctx, anomalyKey).Result()
		s.redis.Expire(ctx, anomalyKey, 8*time.Hour)

		s.analytics.Publish(ctx, "gps.anomaly_detected", "DRIVER", userID, nil, map[string]interface{}{
			"driver_id":          profile.ID,
			"computed_speed_kmh": speed,
		})

		if count >= 3 {
			_ = s.repo.SetApprovalStatus(ctx, profile.ID, "SUSPENDED", "", nil)
			s.log.Warn().Str("driver_id", profile.ID).Msg("driver auto-suspended: 3 GPS anomalies")
		}

		return apperrors.ErrGPSPlausibility
	}

	locJSON, _ := json.Marshal(map[string]interface{}{
		"lat":        update.Lat,
		"lng":        update.Lng,
		"speed_kmh":  update.SpeedKMH,
		"heading":    update.Heading,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	// 120s TTL: clients now throttle pings (idle drivers send a heartbeat only
	// every ~60s), so a 30s TTL would let the location key expire between
	// heartbeats. 120s leaves headroom for a missed beat before the geofence
	// has to fall back to the PostGIS location.
	s.redis.Set(ctx, rkeys.K.DriverLocation(profile.ID), locJSON, 120*time.Second)
	s.redis.LPush(ctx, rkeys.K.DriverLocationHistory(profile.ID), locJSON)
	s.redis.LTrim(ctx, rkeys.K.DriverLocationHistory(profile.ID), 0, 9)

	// Re-assert the driver's presence in the Redis GEO index on EVERY ping.
	//
	// We used to skip this when movement was < 15m (a write-saving "noise
	// filter"), but that left a parked-yet-online driver invisible: if their
	// geo entry was ever dropped (trip handoff, Redis restart/eviction, manual
	// flush) while stationary, no subsequent ping would re-add them and they
	// became unmatchable despite being online. GeoAdd is O(log N) and
	// idempotent for an unchanged position, so always re-adding is the correct
	// trade-off — an online driver who is pinging is always discoverable.
	s.redis.GeoAdd(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), &goredis.GeoLocation{
		Name:      profile.ID,
		Longitude: update.Lng,
		Latitude:  update.Lat,
	})

	// Forward the freshly-persisted position to the customer watching this
	// driver's active ride, if any.
	s.relayLocationToCustomer(ctx, profile.ID, update.Lat, update.Lng)

	// Async telemetry writes
	go func() {
		bgCtx := context.Background()
		if anomaly, speed := s.checkGPSPlausibility(bgCtx, profile.ID, newPoint); anomaly {
			_ = s.repo.LogGPSAnomaly(bgCtx, profile.ID, speed, nil, &newPoint)

			anomalyKey := rkeys.K.GPSAnomalyCount(profile.ID)
			count, _ := s.redis.Incr(bgCtx, anomalyKey).Result()
			s.redis.Expire(bgCtx, anomalyKey, 8*time.Hour)

			s.analytics.Publish(bgCtx, "gps.anomaly_detected", "DRIVER", userID, nil, map[string]interface{}{
				"driver_id":          profile.ID,
				"computed_speed_kmh": speed,
			})

			if count >= 3 {
				_ = s.repo.SetApprovalStatus(bgCtx, profile.ID, "SUSPENDED", "", nil)
				s.log.Warn().Str("driver_id", profile.ID).Msg("driver auto-suspended: 3 GPS anomalies")
			}
			return
		}

		_ = s.repo.UpsertLocation(bgCtx, profile.ID, newPoint, update.SpeedKMH, update.Heading)
		s.maybeAutoMarkArrived(bgCtx, profile.ID, newPoint)
	}()

	return nil
}

// maybeAutoMarkArrived is the server-geofence auto-arrival check: it fires
// only when this driver has an active ride currently DRIVER_EN_ROUTE, gated
// by a single cheap Redis read before ever calling into ride.Service (which
// hits Postgres) — the overwhelming majority of pings (driver has no active
// ride, or the ride isn't in this narrow window) never pay that cost. Called
// from the background telemetry goroutine, never on the request's critical
// path: arrival is a best-effort convenience on top of the manual "Arrived"
// button, which keeps working unchanged as the fallback if this never fires
// or errors.
func (s *Service) maybeAutoMarkArrived(ctx context.Context, driverProfileID string, point geo.Point) {
	if s.arrivalMarker == nil {
		return
	}
	rideID, err := s.redis.Get(ctx, rkeys.K.DriverActiveRide(driverProfileID)).Result()
	if err != nil || rideID == "" {
		return
	}
	// "DRIVER_EN_ROUTE" mirrors ride.StatusDriverEnRoute's string value — this
	// package can't import package ride (see ArrivalMarker's doc), so this is
	// a cheap pre-filter hint only. MarkDriverArrivedIfNear re-checks status
	// authoritatively against Postgres before transitioning anything.
	state, err := s.redis.Get(ctx, rkeys.K.RideState(rideID)).Result()
	if err != nil || state != "DRIVER_EN_ROUTE" {
		return
	}
	if err := s.arrivalMarker.MarkDriverArrivedIfNear(ctx, rideID, driverProfileID, point); err != nil {
		s.log.Warn().Err(err).Str("ride_id", rideID).Str("driver_id", driverProfileID).
			Msg("driver: auto-arrival check failed")
	}
}

// UpdateLocationBatch processes a batch of GPS coordinates from the driver asynchronously.
func (s *Service) UpdateLocationBatch(ctx context.Context, userID string, batch []BatchLocationUpdate) error {
	if len(batch) == 0 {
		return nil
	}

	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if !profile.IsOnline || profile.ApprovalStatus != "APPROVED" {
		return apperrors.ErrDriverNotActive
	}

	// Use the last coordinate in the batch as the most recent live location
	latest := batch[len(batch)-1]
	newPoint := geo.Point{Lat: latest.Lat, Lng: latest.Lng}
	if err := newPoint.Validate(); err != nil {
		return err
	}

	locJSON, _ := json.Marshal(map[string]interface{}{
		"lat":        latest.Lat,
		"lng":        latest.Lng,
		"speed_kmh":  latest.SpeedKMH,
		"heading":    latest.Heading,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	s.redis.Set(ctx, rkeys.K.DriverLocation(profile.ID), locJSON, 120*time.Second)
	s.redis.LPush(ctx, rkeys.K.DriverLocationHistory(profile.ID), locJSON)
	s.redis.LTrim(ctx, rkeys.K.DriverLocationHistory(profile.ID), 0, 9)

	s.redis.GeoAdd(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), &goredis.GeoLocation{
		Name:      profile.ID,
		Longitude: latest.Lng,
		Latitude:  latest.Lat,
	})

	// Forward the freshly-persisted position to the customer watching this
	// driver's active ride, if any.
	s.relayLocationToCustomer(ctx, profile.ID, latest.Lat, latest.Lng)

	// Async telemetry batch writes
	go func() {
		bgCtx := context.Background()
		for _, update := range batch {
			pt := geo.Point{Lat: update.Lat, Lng: update.Lng}
			if err := pt.Validate(); err != nil {
				continue
			}

			// Run GPS plausibility check on the latest update in the batch only
			if update.Lat == latest.Lat && update.Lng == latest.Lng {
				if anomaly, speed := s.checkGPSPlausibility(bgCtx, profile.ID, pt); anomaly {
					_ = s.repo.LogGPSAnomaly(bgCtx, profile.ID, speed, nil, &pt)

					anomalyKey := rkeys.K.GPSAnomalyCount(profile.ID)
					count, _ := s.redis.Incr(bgCtx, anomalyKey).Result()
					s.redis.Expire(bgCtx, anomalyKey, 8*time.Hour)

					s.analytics.Publish(bgCtx, "gps.anomaly_detected", "DRIVER", userID, nil, map[string]interface{}{
						"driver_id":          profile.ID,
						"computed_speed_kmh": speed,
					})

					if count >= 3 {
						_ = s.repo.SetApprovalStatus(bgCtx, profile.ID, "SUSPENDED", "", nil)
						s.log.Warn().Str("driver_id", profile.ID).Msg("driver auto-suspended: 3 GPS anomalies")
					}
					continue
				}
			}

			_ = s.repo.UpsertLocation(bgCtx, profile.ID, pt, update.SpeedKMH, update.Heading)
		}
		// Auto-arrival check against the latest (most recent) point only — the
		// same one already used for plausibility above, not every historical
		// point in the batch.
		s.maybeAutoMarkArrived(bgCtx, profile.ID, newPoint)
	}()

	return nil
}

// relayLocationToCustomer forwards a driver's freshly-persisted GPS position
// to the customer on their active ride, if any. EMA-smoothed (α=0.4) to
// reduce GPS jitter on the customer map without introducing significant lag —
// the raw coordinates are already persisted above for geofence checks; this
// only smooths the customer-facing position.
//
// This used to live in the WS read pump (tracking.Handler.DriverWS), keyed off
// the driver's "location_update" WS frame. The app publishes driver location
// over this REST path, not WS, so that copy was dead code and the customer
// marker never actually streamed — moved here, the path that's actually hit.
func (s *Service) relayLocationToCustomer(ctx context.Context, profileID string, lat, lng float64) {
	if s.wsNotifier == nil {
		return
	}
	rideID, err := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profileID)).Result()
	if err != nil || rideID == "" {
		return
	}

	smoothLat, smoothLng := lat, lng
	const emaAlpha = 0.4
	if prev, perr := s.redis.Get(ctx, rkeys.K.DriverSmoothedLocation(profileID)).Result(); perr == nil {
		var prevLoc struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		}
		if json.Unmarshal([]byte(prev), &prevLoc) == nil && (prevLoc.Lat != 0 || prevLoc.Lng != 0) {
			smoothLat = emaAlpha*lat + (1-emaAlpha)*prevLoc.Lat
			smoothLng = emaAlpha*lng + (1-emaAlpha)*prevLoc.Lng
		}
	}

	smoothJSON, err := json.Marshal(map[string]interface{}{"lat": smoothLat, "lng": smoothLng})
	if err != nil {
		s.log.Warn().Err(err).Str("driver_id", profileID).Msg("driver: failed to marshal smoothed location")
		return
	}
	// Store smoothed position — no expiry, overwritten on every update.
	s.redis.Set(ctx, rkeys.K.DriverSmoothedLocation(profileID), string(smoothJSON), 0)
	// Persist smoothed position for reconnecting customers.
	s.redis.Set(ctx, rkeys.K.RideDriverLocation(rideID), string(smoothJSON), 30*time.Minute)

	s.wsNotifier.NotifyCustomer(rideID, "driver_location", map[string]interface{}{
		"lat": smoothLat,
		"lng": smoothLng,
	})
}

func (s *Service) checkGPSPlausibility(ctx context.Context, driverProfileID string, newPoint geo.Point) (bool, float64) {
	// Outside production, skip plausibility entirely. Developers routinely
	// teleport the simulator (e.g. Cupertino → Kigali), which would otherwise
	// compute an impossible speed, flag a false anomaly, and eventually
	// auto-suspend the test driver. The guard stays fully active in production.
	if s.cfg.Env != "production" {
		return false, 0
	}

	// Skip the check entirely during the go-online grace period (first ~60 s).
	// The mobile app sends the placeholder KIGALI_CENTER position before real
	// device GPS resolves; comparing that to the actual GPS coordinates would
	// compute a physically impossible speed and trigger a false anomaly.
	if _, err := s.redis.Get(ctx, rkeys.K.DriverGracePeriod(driverProfileID)).Result(); err == nil {
		return false, 0
	}

	entries, err := s.redis.LRange(ctx, rkeys.K.DriverLocationHistory(driverProfileID), 0, 0).Result()
	if err != nil || len(entries) == 0 {
		return false, 0
	}
	var prev struct {
		Lat       float64 `json:"lat"`
		Lng       float64 `json:"lng"`
		UpdatedAt string  `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(entries[0]), &prev); err != nil {
		return false, 0
	}
	prevPoint := geo.Point{Lat: prev.Lat, Lng: prev.Lng}
	prevTime, err := time.Parse(time.RFC3339, prev.UpdatedAt)
	if err != nil {
		return false, 0
	}
	elapsed := time.Since(prevTime).Seconds()
	if elapsed <= 0 {
		return false, 0
	}
	if s.cfg.GPS.StaleThresholdSeconds > 0 && elapsed > s.cfg.GPS.StaleThresholdSeconds {
		return false, 0
	}
	speed := geo.SpeedKMH(prevPoint, newPoint, elapsed)
	if speed > s.cfg.GPS.MaxSpeedKMH {
		return true, speed
	}
	return false, speed
}

// RecordDecline handles a driver declining a ride request, applying penalties.
func (s *Service) RecordDecline(ctx context.Context, userID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	key := rkeys.K.DriverDailyDeclines(profile.ID)
	count, _ := s.redis.Incr(ctx, key).Result()
	s.redis.ExpireAt(ctx, key, s.endOfDay())

	s.analytics.Publish(ctx, "driver.declined_request", "DRIVER", userID, nil, map[string]interface{}{
		"driver_id":           profile.ID,
		"daily_decline_count": count,
	})

	switch {
	case int(count) >= s.cfg.Driver.DeclineAutoOfflineThreshold:
		if err := s.repo.UpdateOnlineStatus(ctx, userID, false); err != nil {
			return err
		}
		s.analytics.Publish(ctx, "driver.auto_offline", "DRIVER", userID, nil, map[string]interface{}{
			"driver_id": profile.ID, "reason": "15 daily declines",
		})
		s.log.Warn().Str("driver_id", profile.ID).Msg("driver auto-offlined: 15 declines")

	case int(count) >= s.cfg.Driver.DeclinePriorityThreshold:
		if err := s.repo.SetPriorityTier(ctx, profile.ID, 2); err != nil {
			return err
		}
		s.analytics.Publish(ctx, "driver.priority_demoted", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
	}

	return nil
}

// GetProfile returns the current driver profile.
func (s *Service) GetProfile(ctx context.Context, userID string) (*Profile, error) {
	return s.repo.FindProfileByUserID(ctx, userID)
}

// GetDailyEarnings returns today's driver payout and the number of rides
// completed today — "today" being the local calendar day in the platform
// timezone, so the figure resets at local midnight and never counts a ride
// twice or shrinks as rides age.
func (s *Service) GetDailyEarnings(ctx context.Context, driverUserID string) (float64, int, error) {
	start, end := timeutil.DayWindow(time.Now(), s.cfg.Location())
	gross, count, err := s.repo.GetEarnings(ctx, driverUserID, start, end)
	if err != nil {
		return 0, 0, err
	}
	return CalculateDriverPayout(gross), count, nil
}

// GetWeeklyEarnings returns the payout across the last 7 local calendar days,
// today included.
func (s *Service) GetWeeklyEarnings(ctx context.Context, driverUserID string) (float64, error) {
	start, end := timeutil.DaysWindow(time.Now(), s.cfg.Location(), 7)
	gross, _, err := s.repo.GetEarnings(ctx, driverUserID, start, end)
	if err != nil {
		return 0, err
	}
	return CalculateDriverPayout(gross), nil
}

func CalculateDriverPayout(grossFare float64) float64 {
	return grossFare * DriverPayoutRate
}

// GetStats returns driver performance statistics.
func (s *Service) GetStats(ctx context.Context, driverUserID string) (map[string]interface{}, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, driverUserID)
	if err != nil {
		return nil, err
	}

	completionRate, err := s.repo.GetCompletionRate(ctx, profile.ID)
	if err != nil {
		completionRate = 0
	}

	return map[string]interface{}{
		"total_rides":     profile.TotalRides,
		"acceptance_rate": profile.AcceptanceRate,
		"completion_rate": completionRate,
		"priority_tier":   profile.PriorityTier,
	}, nil
}

// allVehicleTypes lists every vehicle type the platform supports.
var allVehicleTypes = []string{"MOTO_BIKE", "CAB_TAXI", "HEAVY_FUSO", "LIGHT_HILUX", "TUK_TUK"}

const (
	driverStateAvailable = "AVAILABLE"
	// The nearby-driver preview radius matches the matching engine's expanded
	// reach (10 km) so a driver the customer could actually be matched with also
	// shows on the map. A narrower preview made online drivers look absent.
	nearbySearchRadiusKM = 10.0
	nearbySearchRadiusM  = 10000
	nearbyMaxPerType     = 6
	// Kigali city-average speed used for ETA estimation without a routing call.
	citySpeedKMH = 25.0
)

// GetNearbyDrivers returns anonymised nearby drivers for a customer location.
// If transportType is empty, all vehicle types are queried in a single call.
//
// Primary source: Redis GEO (real-time, sub-millisecond per type).
// Fallback:       PostGIS driver_locations (cold-start, Redis flush).
//
// Each result includes an estimated ETA computed from straight-line distance
// at city-average speed — no routing API call needed.
func (s *Service) GetNearbyDrivers(ctx context.Context, loc geo.Point, transportType string) ([]*NearbyDriver, error) {
	types := allVehicleTypes
	if transportType != "" {
		types = []string{transportType}
	}

	var result []*NearbyDriver
	for _, tt := range types {
		drivers := s.nearbyForType(ctx, loc, tt)
		result = append(result, drivers...)
	}
	return result, nil
}

// nearbyForType queries one vehicle type: Redis GEO first, PostGIS fallback.
func (s *Service) nearbyForType(ctx context.Context, loc geo.Point, transportType string) []*NearbyDriver {
	// ── 1. Redis GEO — real-time, O(log N + k) ───────────────────────────────
	geoKey := rkeys.K.DriverGeoIndex(transportType)
	geoResults, err := s.redis.GeoSearchLocation(ctx, geoKey, &goredis.GeoSearchLocationQuery{
		GeoSearchQuery: goredis.GeoSearchQuery{
			Longitude:  loc.Lng,
			Latitude:   loc.Lat,
			Radius:     nearbySearchRadiusKM,
			RadiusUnit: "km",
			Sort:       "ASC",
			Count:      nearbyMaxPerType + 4, // fetch extra to allow for state filtering
		},
		WithCoord: true,
		WithDist:  true,
	}).Result()

	if err == nil && len(geoResults) > 0 {
		var drivers []*NearbyDriver
		for _, r := range geoResults {
			if len(drivers) >= nearbyMaxPerType {
				break
			}
			// Skip drivers not in AVAILABLE state (ON_TRIP, OFFLINE, matching-locked).
			state, _ := s.redis.Get(ctx, rkeys.K.DriverState(r.Name)).Result()
			if state != driverStateAvailable {
				continue
			}
			distM := r.Dist * 1000
			drivers = append(drivers, &NearbyDriver{
				TransportType: transportType,
				DistanceM:     distM,
				ApproxLat:     r.Latitude + jitter(),
				ApproxLng:     r.Longitude + jitter(),
				ETAMinutes:    etaMinutes(distM),
			})
		}
		if len(drivers) > 0 {
			return drivers
		}
	}

	// ── 2. PostGIS fallback — handles cold-start / Redis flush ────────────────
	candidates, err := s.repo.FindNearby(ctx, loc, nearbySearchRadiusM, transportType, nil)
	if err != nil || len(candidates) == 0 {
		return nil
	}
	var fallback []*NearbyDriver
	for _, c := range candidates {
		if len(fallback) >= nearbyMaxPerType {
			break
		}
		fallback = append(fallback, &NearbyDriver{
			TransportType: transportType,
			DistanceM:     c.DistanceM,
			ApproxLat:     c.Lat + jitter(),
			ApproxLng:     c.Lng + jitter(),
			ETAMinutes:    etaMinutes(c.DistanceM),
		})
	}
	return fallback
}

// etaMinutes estimates arrival time from straight-line distance at city speed.
// Good enough for the pre-booking map view; avoids a routing API call per driver.
func etaMinutes(distanceM float64) int {
	if distanceM <= 0 {
		return 1
	}
	minutes := (distanceM / 1000.0) / citySpeedKMH * 60.0
	eta := int(math.Ceil(minutes))
	if eta < 1 {
		return 1
	}
	return eta
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "23505") || contains(msg, "unique")
}

// mapApplyErr translates a driver_profiles/users write error from
// CreateProfile or UpdateProfileForResubmission into the client-facing error
// it represents. Shared by both call sites in Apply so the new-application
// and resubmission paths can never drift on how they report a duplicate.
//
// ErrNationalIDTaken (uq_users_national_id, 23505) is checked FIRST and
// specifically, ahead of the generic isUniqueViolation substring match — a
// national ID collision must always get the "already registered" message,
// never be swallowed by the generic "vehicle plate or license number already
// registered" one meant for driver_profiles' own unique columns.
func mapApplyErr(err error) error {
	if errors.Is(err, ErrNationalIDTaken) {
		return apperrors.New(http.StatusConflict, "NATIONAL_ID_ALREADY_REGISTERED",
			"This national ID is already registered to another account.")
	}
	if isUniqueViolation(err) {
		return apperrors.New(409, "DUPLICATE_CREDENTIALS", "vehicle plate or license number already registered")
	}
	return err
}

func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// endOfDay is when the current day's counters stop counting — local midnight in
// the platform timezone, not UTC midnight (which lands at 02:00 in Kigali and
// would reset a night driver's counters mid-shift).
func (s *Service) endOfDay() time.Time {
	return timeutil.EndOfLocalDay(time.Now(), s.cfg.Location())
}

// jitter adds a small random offset to driver coordinates before sending them
// to customers. This prevents customers from pinpointing a driver's exact location
// before a ride is booked (privacy), while keeping the map marker believable.
// ±0.0015° ≈ ±165 m per axis — large enough for privacy, small enough to stay
// within the same city block so the marker looks correct on the map.
func jitter() float64 {
	return (rand.Float64() - 0.5) * 0.003
}
