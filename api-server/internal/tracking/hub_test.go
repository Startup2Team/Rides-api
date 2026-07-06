package tracking

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

func TestHub_RedisPubSubBroadcast(t *testing.T) {
	s := miniredis.RunT(t)

	rdb := goredis.NewClient(&goredis.Options{
		Addr: s.Addr(),
	})
	defer rdb.Close()

	logger := zerolog.Nop()
	hub := NewHub(rdb, logger)
	defer hub.Close()

	// 1. Test driver connection and pub/sub broadcast
	driverSend := make(chan Message, 10)
	driverDone := make(chan struct{})
	driverClient := &Client{
		UserID: "driver-123",
		Role:   "DRIVER",
		Send:   driverSend,
		done:   driverDone,
	}

	hub.RegisterDriver("driver-123", driverClient)

	// Wait briefly for PSubscribe subscription to activate
	time.Sleep(100 * time.Millisecond)

	msg := Message{
		Type: "test_driver_notification",
		Payload: map[string]interface{}{
			"hello": "world",
		},
	}

	hub.SendToDriver("driver-123", msg)

	// Read message from local driver channel
	select {
	case received := <-driverSend:
		if received.Type != "test_driver_notification" {
			t.Errorf("expected msg type test_driver_notification, got %s", received.Type)
		}
		if val, ok := received.Payload["hello"].(string); !ok || val != "world" {
			t.Errorf("expected payload hello: world, got %v", received.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for driver pub/sub message")
	}

	// 2. Test customer connection and pub/sub broadcast
	customerSend := make(chan Message, 10)
	customerDone := make(chan struct{})
	customerClient := &Client{
		UserID: "customer-456",
		RideID: "ride-789",
		Role:   "CUSTOMER",
		Send:   customerSend,
		done:   customerDone,
	}

	hub.RegisterCustomer("ride-789", "customer-456", customerClient)

	time.Sleep(100 * time.Millisecond)

	msgCust := Message{
		Type:   "test_customer_notification",
		RideID: "ride-789",
	}

	hub.SendToCustomer("ride-789", msgCust)

	select {
	case received := <-customerSend:
		if received.Type != "test_customer_notification" {
			t.Errorf("expected msg type test_customer_notification, got %s", received.Type)
		}
		if received.RideID != "ride-789" {
			t.Errorf("expected rideID ride-789, got %s", received.RideID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for customer pub/sub message")
	}

	// 3. Test active connections count
	drivers, customers := hub.ActiveConnectionsCount()
	if drivers != 1 {
		t.Errorf("expected 1 driver, got %d", drivers)
	}
	if customers != 1 {
		t.Errorf("expected 1 customer, got %d", customers)
	}

	hub.UnregisterDriver("driver-123")
	hub.UnregisterCustomer("ride-789")

	drivers, customers = hub.ActiveConnectionsCount()
	if drivers != 0 {
		t.Errorf("expected 0 drivers, got %d", drivers)
	}
	if customers != 0 {
		t.Errorf("expected 0 customers, got %d", customers)
	}
}
