// Package documents is the single source of truth for driver KYC document types
// and whether each one describes a person or a vehicle.
//
// This existed twice before, and the two copies disagreed: internal/driver
// validated against a `oneof` tag listing SELFIE, while internal/admin checked a
// map listing PROFILE_SELFIE plus VEHICLE_INSURANCE_BACK and
// VEHICLE_AUTHORIZATION_BACK that the driver path rejected. The same document
// therefore had two names depending on who uploaded it, and neither side could
// reliably find what the other had stored. Migration 078 adds a CHECK constraint
// over exactly this vocabulary, so a value that is valid here and nowhere else
// now fails at the database rather than becoming an orphan row.
package documents

// Type is a driver document type as stored in driver_documents.document_type.
type Type = string

// Person-level documents. These describe the driver, so they are shared across
// every vehicle they operate and carry vehicle_id = NULL.
const (
	NationalIDFront Type = "NATIONAL_ID_FRONT"
	NationalIDBack  Type = "NATIONAL_ID_BACK"
	LicenceFront    Type = "LICENCE_FRONT"
	LicenceBack     Type = "LICENCE_BACK"
	Selfie          Type = "SELFIE"
)

// Vehicle-level documents. These describe one specific vehicle and REQUIRE a
// vehicle_id — a driver with two cars needs two insurance documents, and before
// 078 the second silently superseded the first.
const (
	VehicleInsurance         Type = "VEHICLE_INSURANCE"
	VehicleInsuranceBack     Type = "VEHICLE_INSURANCE_BACK"
	VehicleAuthorization     Type = "VEHICLE_AUTHORIZATION"
	VehicleAuthorizationBack Type = "VEHICLE_AUTHORIZATION_BACK"
)

var personTypes = map[Type]bool{
	NationalIDFront: true,
	NationalIDBack:  true,
	LicenceFront:    true,
	LicenceBack:     true,
	Selfie:          true,
}

var vehicleTypes = map[Type]bool{
	VehicleInsurance:         true,
	VehicleInsuranceBack:     true,
	VehicleAuthorization:     true,
	VehicleAuthorizationBack: true,
}

// aliases maps historical spellings onto the canonical type. The admin panel
// wrote PROFILE_SELFIE for what mobile called SELFIE; 078 rewrites stored rows,
// and Normalize keeps older clients working instead of 400ing them.
var aliases = map[Type]Type{
	"PROFILE_SELFIE": Selfie,
}

// Normalize maps a possibly-legacy type onto its canonical form. Unknown values
// pass through unchanged so the caller reports "unsupported type" rather than
// this function inventing one.
func Normalize(t Type) Type {
	if canonical, ok := aliases[t]; ok {
		return canonical
	}
	return t
}

// IsValid reports whether t is a known document type, after normalisation.
func IsValid(t Type) bool {
	t = Normalize(t)
	return personTypes[t] || vehicleTypes[t]
}

// RequiresVehicle reports whether t describes a specific vehicle and therefore
// must be stored with a vehicle_id. Mirrors driver_documents_type_scope_chk.
func RequiresVehicle(t Type) bool {
	return vehicleTypes[Normalize(t)]
}

// All returns every canonical type, person-level first. Order is stable so it is
// safe to render in a UI or a fixture.
func All() []Type {
	return []Type{
		NationalIDFront, NationalIDBack,
		LicenceFront, LicenceBack,
		Selfie,
		VehicleInsurance, VehicleInsuranceBack,
		VehicleAuthorization, VehicleAuthorizationBack,
	}
}
