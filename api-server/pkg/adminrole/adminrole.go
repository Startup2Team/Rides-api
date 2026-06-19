// Package adminrole defines the five admin role codes embedded in JWTs.
// These codes are derived from the human-readable role names stored in
// admin_roles.name and encoded as stable, comparison-safe enums.
package adminrole

const (
	SuperAdmin     = "SUPER_ADMIN"
	OpsManager     = "OPS_MANAGER"
	FinanceManager = "FINANCE_MANAGER"
	SupportStaff   = "SUPPORT_STAFF"
	AnalyticsStaff = "ANALYTICS_STAFF"
)

// All lists every valid admin role code.
var All = []string{SuperAdmin, OpsManager, FinanceManager, SupportStaff, AnalyticsStaff}

// FromRoleName converts the human-readable role name stored in admin_roles.name
// to its stable enum code embedded in JWTs and used by middleware guards.
func FromRoleName(name string) string {
	switch name {
	case "Super Admin":
		return SuperAdmin
	case "Operations Manager":
		return OpsManager
	case "Finance Manager":
		return FinanceManager
	case "Support Staff":
		return SupportStaff
	case "Analytics Staff":
		return AnalyticsStaff
	default:
		return ""
	}
}
