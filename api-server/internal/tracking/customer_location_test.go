package tracking

import "testing"

// injectCustomerLocation backs the driver reconnect replay's optional
// customer_lat/customer_lng fields (item 4 of the customer-location
// contract): a driver reconnecting mid-trip should see the customer's last
// known position without waiting for the next publish, but a missing or
// malformed cache entry must never break the replay.

func TestInjectCustomerLocation_ValidJSON(t *testing.T) {
	payload := map[string]interface{}{"status": "IN_PROGRESS", "ride_id": "ride-1"}

	injectCustomerLocation(payload, `{"lat":-1.9441,"lng":30.0619}`)

	if payload["customer_lat"] != -1.9441 {
		t.Errorf("expected customer_lat -1.9441, got %v", payload["customer_lat"])
	}
	if payload["customer_lng"] != 30.0619 {
		t.Errorf("expected customer_lng 30.0619, got %v", payload["customer_lng"])
	}
	// Existing keys must survive untouched.
	if payload["status"] != "IN_PROGRESS" {
		t.Errorf("expected status to be preserved, got %v", payload["status"])
	}
}

func TestInjectCustomerLocation_MalformedJSONIsNoOp(t *testing.T) {
	payload := map[string]interface{}{"status": "IN_PROGRESS"}

	injectCustomerLocation(payload, `not-json`)

	if _, ok := payload["customer_lat"]; ok {
		t.Error("expected no customer_lat on malformed input")
	}
	if _, ok := payload["customer_lng"]; ok {
		t.Error("expected no customer_lng on malformed input")
	}
}

func TestInjectCustomerLocation_EmptyStringIsNoOp(t *testing.T) {
	payload := map[string]interface{}{}

	injectCustomerLocation(payload, "")

	if len(payload) != 0 {
		t.Errorf("expected payload untouched, got %v", payload)
	}
}
