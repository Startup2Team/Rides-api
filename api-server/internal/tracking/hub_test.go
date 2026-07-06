package tracking

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// The core horizontal-scale test: a driver connected to instance B must receive
// a message generated on instance A. Without Redis fan-out this silently drops
// (A's in-memory map has no socket for the driver). Two hubs + one Redis = two
// boxes behind a load balancer.
func TestHub_FanoutDeliversToSocketOnAnotherInstance(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	newClient := func() *redis.Client { return redis.NewClient(&redis.Options{Addr: mr.Addr()}) }

	hubA := NewHub(zerolog.Nop(), newClient())
	hubB := NewHub(zerolog.Nop(), newClient())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hubB.Run(ctx)                   // only B subscribes to the fan-out channel
	time.Sleep(200 * time.Millisecond) // let B's subscription establish

	// The driver's socket lives on instance B.
	client := &Client{UserID: "driver-1", Role: "DRIVER", Send: make(chan Message, 1)}
	hubB.RegisterDriver("driver-1", client)

	// The "you got a ride" message is produced on instance A, which has no socket.
	hubA.SendToDriver("driver-1", Message{Type: "ride.matched", RideID: "r1"})

	select {
	case got := <-client.Send:
		if got.Type != "ride.matched" || got.RideID != "r1" {
			t.Fatalf("wrong message delivered across instances: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message was NOT delivered across instances — fan-out is broken")
	}
}

// Same for a customer socket, keyed by ride id.
func TestHub_FanoutDeliversToCustomerOnAnotherInstance(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	newClient := func() *redis.Client { return redis.NewClient(&redis.Options{Addr: mr.Addr()}) }

	hubA := NewHub(zerolog.Nop(), newClient())
	hubB := NewHub(zerolog.Nop(), newClient())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hubB.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	client := &Client{UserID: "cust-1", RideID: "ride-9", Role: "CUSTOMER", Send: make(chan Message, 1)}
	hubB.RegisterCustomer("ride-9", "cust-1", client)

	hubA.SendToCustomer("ride-9", Message{Type: "driver.location", RideID: "ride-9"})

	select {
	case got := <-client.Send:
		if got.Type != "driver.location" {
			t.Fatalf("wrong message: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("customer message was NOT delivered across instances")
	}
}

// A message for a socket on THIS instance is delivered exactly once (locally);
// the instance must ignore its own Redis echo (no double delivery).
func TestHub_NoDoubleDeliveryFromOwnEcho(t *testing.T) {
	mr, _ := miniredis.Run()
	defer mr.Close()
	hub := NewHub(zerolog.Nop(), redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	client := &Client{UserID: "d1", Send: make(chan Message, 4)}
	hub.RegisterDriver("d1", client)
	hub.SendToDriver("d1", Message{Type: "x"})

	select {
	case <-client.Send: // the single local delivery
	case <-time.After(2 * time.Second):
		t.Fatal("no local delivery")
	}
	// The instance's own echo comes back over Redis — it must be ignored.
	time.Sleep(200 * time.Millisecond)
	select {
	case extra := <-client.Send:
		t.Fatalf("double delivery from own echo: %+v", extra)
	default:
	}
}

// Fan-out must be resilient: with no Redis wired (nil), delivery is purely local
// and Send/Run never panic.
func TestHub_NilRedisStaysLocal(t *testing.T) {
	hub := NewHub(zerolog.Nop(), nil)
	go hub.Run(context.Background()) // no-op, must not panic
	client := &Client{UserID: "d1", Send: make(chan Message, 1)}
	hub.RegisterDriver("d1", client)
	hub.SendToDriver("d1", Message{Type: "local"})
	select {
	case got := <-client.Send:
		if got.Type != "local" {
			t.Fatalf("wrong message: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("local delivery failed with nil redis")
	}
}
