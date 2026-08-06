package documents

import "testing"

func TestSplitMatchesTheCheckConstraint(t *testing.T) {
	for _, ty := range All() {
		if !IsValid(ty) {
			t.Fatalf("All() returned %q which IsValid rejects", ty)
		}
	}
	// Every type must be exactly one of person-level or vehicle-level — the CHECK
	// constraint in 078 has no third branch, so a type in neither map (or both)
	// would be accepted here and rejected by the database.
	for _, ty := range All() {
		p, v := personTypes[ty], vehicleTypes[ty]
		if p == v {
			t.Fatalf("%q is in neither or both scopes (person=%v vehicle=%v)", ty, p, v)
		}
		if RequiresVehicle(ty) != v {
			t.Fatalf("%q: RequiresVehicle=%v but vehicleTypes=%v", ty, RequiresVehicle(ty), v)
		}
	}
}

func TestLegacySelfieAliasNormalizes(t *testing.T) {
	if got := Normalize("PROFILE_SELFIE"); got != Selfie {
		t.Fatalf("PROFILE_SELFIE should normalize to %q, got %q", Selfie, got)
	}
	if !IsValid("PROFILE_SELFIE") {
		t.Fatal("the legacy admin spelling must still be accepted")
	}
	if RequiresVehicle("PROFILE_SELFIE") {
		t.Fatal("a selfie is person-level even under its legacy name")
	}
}

func TestUnknownTypeIsRejectedNotInvented(t *testing.T) {
	if IsValid("VEHICLE_LOGBOOK") {
		t.Fatal("unknown types must be rejected")
	}
	if got := Normalize("VEHICLE_LOGBOOK"); got != "VEHICLE_LOGBOOK" {
		t.Fatalf("Normalize must pass unknowns through unchanged, got %q", got)
	}
}
