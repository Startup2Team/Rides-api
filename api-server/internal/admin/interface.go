package admin

import "context"

// AdminService is the interface that Handler depends on.
// *Service satisfies this interface automatically via Go structural typing.
// Methods must stay in sync with service.go and origin/dev.
type AdminService interface {
	ListDrivers(ctx context.Context, status, vehicleType, search, sort string, limit, offset int) ([]map[string]interface{}, int, error)
	DriverOverview(ctx context.Context, vehicleType string) (map[string]interface{}, error)
	CustomerOverview(ctx context.Context) (map[string]interface{}, error)
	NegotiationsStats(ctx context.Context) (map[string]interface{}, error)
	CreateDriverFromAdmin(ctx context.Context, in AdminCreateDriverInput) (map[string]interface{}, error)
	ForceDriverOffline(ctx context.Context, profileID string) error
	LiveRidesStats(ctx context.Context) (map[string]interface{}, error)
	ApproveDriver(ctx context.Context, profileID, adminUserID string) error
	RejectDriver(ctx context.Context, profileID, adminUserID, reason string) error
	SuspendDriver(ctx context.Context, profileID, adminUserID, reason string, durationHours int) error
	ReinstateDriver(ctx context.Context, profileID string) error
	GetDriver(ctx context.Context, profileID string) (map[string]interface{}, error)
	UpdateDriver(ctx context.Context, profileID string, fields map[string]interface{}) error
	DeleteDriver(ctx context.Context, profileID string) error
	ListCustomers(ctx context.Context, status, search, sort string, limit, offset int) ([]map[string]interface{}, int, error)
	GetCustomer(ctx context.Context, userID string) (map[string]interface{}, error)
	SuspendUser(ctx context.Context, userID string, durationHours int) error
	ReinstateUser(ctx context.Context, userID string) error
	UpdateCustomer(ctx context.Context, userID, status, notes string) error
	BanCustomer(ctx context.Context, userID, reason string) error
	ListRides(ctx context.Context, status, transportType, search string, limit, offset int) ([]map[string]interface{}, int, error)
	GetRide(ctx context.Context, rideID string) (map[string]interface{}, error)
	ListNegotiations(ctx context.Context, status, search string, limit, offset int) ([]map[string]interface{}, int, error)
	GetNegotiation(ctx context.Context, rideID string) (map[string]interface{}, error)
	RevenueKPIs(ctx context.Context, period string) (map[string]interface{}, error)
	ListTransactions(ctx context.Context, txStatus, sort string, limit, offset int) ([]map[string]interface{}, int, error)
	Revenue(ctx context.Context, period string) (map[string]interface{}, error)
	DisbursePayouts(ctx context.Context, transactionIDs []string) (int, float64, error)
	GPSAnomalies(ctx context.Context, limit int) ([]map[string]interface{}, error)
	DeviceCollisions(ctx context.Context) ([]map[string]interface{}, error)
	ListLiveRides(ctx context.Context, status, district, search string, limit, offset int) ([]map[string]interface{}, int, error)
	GetLiveRide(ctx context.Context, rideID string) (map[string]interface{}, error)
	InterveneRide(ctx context.Context, rideID, action, reason string) error
}
