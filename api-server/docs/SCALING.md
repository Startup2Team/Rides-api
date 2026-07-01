# Scaling Guide — 150M+ Users Architecture

This document details the scaling architecture and production designs of the Taravelis/Rides API, optimized to support **150M+ registered users** (~50M drivers and ~100M customers).

---

## 1. High-Scale Architecture overview

```
                                  ┌───────────────────────────┐
                                  │      Phone / Browser      │
                                  └─────────────┬─────────────┘
                                                │ HTTPS / WSS
                                                ▼
                                    ┌──────────────────────┐
                                    │    Load Balancers    │
                                    └───────────┬──────────┘
                                                │
                 ┌──────────────────────────────┼──────────────────────────────┐
                 ▼ (Pod 1)                      ▼ (Pod 2)                      ▼ (Pod N)
     ┌──────────────────────┐       ┌──────────────────────┐       ┌──────────────────────┐
     │    Go API Server     │       │    Go API Server     │       │    Go API Server     │
     └─────┬───────────┬────┘       └─────┬───────────┬────┘       └─────┬───────────┬────┘
           │           │                  │           │                  │           │
           │           └───────┐          │   ┌───────┘                  │   ┌───────┘
           ▼                   ▼          ▼   ▼                          ▼   ▼
 ┌───────────┐           ┌───────────────────────┐                 ┌───────────┐
 │ PG Write  │           │     Redis Cluster     │                 │  PG Read  │
 │ (Primary) │◄──────────┤ (Pub/Sub & Geo Index) ├────────────────►│ (Replica) │
 └───────────┘ Sync      └───────────────────────┘                 └───────────┘
               Replication
```

The system employs a horizontally stateless modular monolith design where:
1. **HTTP/WS API instances** run statelessly across any number of nodes/Kubernetes pods behind a round-robin load balancer.
2. **WebSocket connections** are scaled horizontally across nodes using a **Redis Pub/Sub backplane** to sync coordinates and events.
3. **Database operations** are split: all state mutations hit the **PostgreSQL primary (write) pool**, while resource-heavy analytics queries, snapshots, and dashboards are routed to **PostgreSQL read replicas**.
4. **Caching, sessions, and live driver locations** use a distributed **Redis Cluster** to eliminate single-point memory bottlenecks.

---

## 2. Horizontal WebSocket scaling (Redis Pub/Sub backplane)

WS connections are process-local. To allow drivers and customers connected to different server pods to communicate and receive real-time location updates, the platform leverages **Redis pattern subscriptions** (`ws:driver:*` and `ws:ride:*`):

- **Wildcard Subscriptions**: When a tracking `Hub` starts, a background routine spawns `rdb.PSubscribe(ctx, "ws:driver:*", "ws:ride:*")` to consume updates published across the entire cluster.
- **Local Routing**: Messages received from the Redis subscription are deserialized. If the target client is connected to the local pod, it is forwarded directly to the client's `Send` channel; otherwise, it is ignored by that pod, minimizing memory overhead.
- **Idempotency & Connection Count**: The local connection count is tracked locally on each pod, and aggregate cluster-wide metrics can be monitored via the `/metrics` endpoint.

---

## 3. Database connection & read path replica

To support 150M+ users without database starvation, queries are isolated:
- **Write Path**: Handled via `DATABASE_URL` (Primary instance).
- **Read Path**: Handled via `DATABASE_READ_URL` (Read replica). 
- **replica-routed queries**: The `analytics.Repository` and `dashboard.Service` run exclusively on the replica pool (`dbRead`). This prevents reporting/analytical sweeps from locking tables or consuming connection slots needed for hot-path matching and ride negotiations.

---

## 4. Redis cluster scaling

To support millions of concurrent online driver locations, Redis is run in **Cluster Mode**:
- **Universal Client**: The backend initializes a `redis.UniversalClient` (via `pkg/redis/redis.go`). 
- **REDIS_CLUSTER_MODE**: Setting `REDIS_CLUSTER_MODE=true` builds a `redis.ClusterClient` which automatically distributes keys across shards based on hash tags.
- **Standalone compatibility**: In development/local environments, `REDIS_CLUSTER_MODE=false` constructs a standard `*redis.Client` transparently.

---

## 5. Async telemetry & Batch ingestion

High-frequency GPS updates are a major bottleneck. Taravelis implements two mitigations:
1. **Non-blocking DB Telemetry**: Both `/api/v1/driver/location` (single) and `/api/v1/driver/locations` (batch) perform the critical Redis state writes (GEO addition, current coordinates cache) *synchronously* to make drivers immediately matchable, but perform all persistent database writes (`LogGPSAnomaly`, `UpsertLocation`) *asynchronously* in a background goroutine.
2. **Batch Locations API**: Drivers can aggregate telemetry coordinates and send them via `POST /api/v1/driver/locations` as a JSON array (`[{lat, lng, speed_kmh, heading, timestamp}]`). Only the *latest* coordinate is written synchronously to Redis, and the entire historical track is written asynchronously to PostGIS.

---

## 6. Index optimization

To prevent table scans on millions of rides and events, the database is optimized with index structures in migration `056_scale_indexes.up.sql`:
- **Active Rides Partial Index**: `idx_rides_active` on `rides` filtering out `COMPLETED` and `CANCELLED`. This keeps the active lookup table tiny.
- **Historical Ride composite indexes**: `idx_rides_driver_status` and `idx_rides_customer_status` speed up dashboard lists.
- **Active Negotiation index**: `idx_negotiation_rounds_ride_round` speeds up bidding queries.
- **Online Driver partial index**: `idx_driver_profiles_online_matching` speeds up matching candidate queries.
