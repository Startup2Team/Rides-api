# Diagrams

All diagrams are written in Mermaid so they can render in GitHub, many IDE previews, and documentation platforms.

## 1. System Context

```mermaid
flowchart LR
  Customer["Customer Mobile App"]
  Driver["Driver Mobile App"]
  Admin["Admin Dashboard"]
  API["Taravelis Go API"]
  Postgres[("PostgreSQL + PostGIS")]
  Redis[("Redis")]
  ObjectStore[("MinIO / CDN")]
  AT["Africa's Talking"]
  FCM["Firebase Cloud Messaging"]
  Maps["Mapbox / Maps Provider"]

  Customer -->|"HTTP / WebSocket"| API
  Driver -->|"HTTP / WebSocket"| API
  Admin -->|"HTTP"| API
  API --> Postgres
  API --> Redis
  API --> ObjectStore
  API --> AT
  API --> FCM
  Customer --> Maps
  Driver --> Maps
```

## 2. Container Diagram

```mermaid
flowchart TB
  subgraph Clients
    CustomerApp["Customer App"]
    DriverApp["Driver App"]
    AdminWeb["Admin Web"]
  end

  subgraph Backend
    APIServer["Go API Container"]
    Router["Chi Router + Middleware"]
    Domain["Domain Modules"]
    WS["WebSocket Hub"]
    AnalyticsConsumer["Analytics Consumer"]
  end

  subgraph Data
    PG[("PostgreSQL/PostGIS")]
    RDS[("Redis")]
    OBJ[("Object Storage")]
  end

  CustomerApp --> APIServer
  DriverApp --> APIServer
  AdminWeb --> APIServer
  APIServer --> Router
  Router --> Domain
  Domain --> PG
  Domain --> RDS
  Domain --> WS
  AnalyticsConsumer --> RDS
  AnalyticsConsumer --> PG
  Domain --> OBJ
```

## 3. Backend Component Diagram

```mermaid
flowchart LR
  Server["cmd/server"]
  Auth["internal/auth"]
  Customer["internal/customer"]
  Driver["internal/driver"]
  Ride["internal/ride"]
  Matching["internal/matching"]
  Negotiation["internal/negotiation"]
  Location["internal/location"]
  Admin["internal/admin"]
  Analytics["internal/analytics"]
  Tracking["internal/tracking"]
  Middleware["internal/middleware"]
  Notify["internal/notification"]
  Telephony["internal/telephony"]
  Payment["internal/payment"]
  Shared["pkg/*"]

  Server --> Middleware
  Server --> Auth
  Server --> Customer
  Server --> Driver
  Server --> Ride
  Server --> Matching
  Server --> Negotiation
  Server --> Location
  Server --> Admin
  Server --> Analytics
  Server --> Tracking
  Server --> Notify
  Server --> Telephony
  Server --> Payment

  Matching --> Ride
  Matching --> Driver
  Matching --> Tracking
  Negotiation --> Ride
  Negotiation --> Tracking
  Ride --> Tracking
  Ride --> Analytics
  Ride --> Location
  Driver --> Analytics
  Auth --> Telephony

  Auth --> Shared
  Driver --> Shared
  Ride --> Shared
  Matching --> Shared
  Negotiation --> Shared
  Location --> Shared
  Admin --> Shared
```

## 4. Class Diagram

```mermaid
classDiagram
  class AuthHandler {
    +Register()
    +VerifyOTP()
    +Refresh()
    +Logout()
  }
  class AuthService {
    +InitiateOTP()
    +VerifyOTP()
    +RefreshTokens()
    +Logout()
  }
  class AuthRepository {
    +CreateOTP()
    +FindLatestOTP()
    +CreateUser()
    +FindUserByPhone()
    +LogDeviceSession()
  }

  class DriverHandler {
    +Apply()
    +GetProfile()
    +UpdateProfile()
    +SetAvailability()
    +UpdateLocation()
    +UploadDocument()
    +AcceptPolicy()
    +DailyEarnings()
  }
  class DriverService {
    +Apply()
    +SetAvailability()
    +UpdateLocation()
    +RecordDecline()
    +GetDailyEarnings()
    +GetWeeklyEarnings()
    +GetStats()
  }
  class DriverRepository {
    +CreateProfile()
    +FindProfileByUserID()
    +UpsertDocument()
    +UpsertLocation()
    +FindNearby()
    +GetEarnings()
  }

  class RideHandler {
    +CreateRide()
    +GetRide()
    +ListRides()
    +CancelRide()
  }
  class RideService {
    +CreateRide()
    +SetMatchingEngine()
    +SetRouteFareRecorder()
    +CancelRide()
    +SetEnRoute()
    +SetDriverArrived()
    +CancelAfterPickupExpiry()
    +StartRide()
    +CompleteRide()
  }
  class RideRepository {
    +CreateRide()
    +FindByID()
    +FindByIDAndCustomer()
    +FindByIDAndDriver()
    +Transition()
    +LockFare()
    +SetDriverArrived()
    +SetCompleted()
    +Cancel()
  }

  class MatchingEngine {
    +StartSearch()
    +NotifyAccept()
    +ValidateAcceptTTL()
  }

  class NegotiationHandler {
    +Propose()
    +Accept()
    +Decline()
    +LockManualFare()
    +InitiateCall()
  }
  class NegotiationService {
    +Propose()
    +Accept()
    +Decline()
    +LockManualFare()
    +InitiateCall()
  }
  class NegotiationRepository {
    +CreateRound()
    +CountRounds()
    +CountRoundsByRole()
    +GetLatestRound()
    +SetResponse()
  }

  class LocationService {
    +GetRoute()
    +UpsertRoute()
    +RecordAgreedFare()
    +GetLandmarks()
    +GetSuggestions()
    +CreateSavedLocation()
    +SwitchMode()
  }

  class TrackingHub {
    +RegisterDriver()
    +RegisterCustomer()
    +SendToDriver()
    +SendToCustomer()
  }

  AuthHandler --> AuthService
  AuthService --> AuthRepository
  DriverHandler --> DriverService
  DriverService --> DriverRepository
  RideHandler --> RideService
  RideService --> RideRepository
  RideService --> MatchingEngine
  RideService --> LocationService
  MatchingEngine --> RideRepository
  MatchingEngine --> DriverRepository
  MatchingEngine --> TrackingHub
  NegotiationHandler --> NegotiationService
  NegotiationService --> NegotiationRepository
  NegotiationService --> RideRepository
  NegotiationService --> TrackingHub
  RideService --> TrackingHub
```

## 5. ERD

```mermaid
erDiagram
  USERS ||--o| DRIVER_PROFILES : "may apply"
  USERS ||--o{ RIDES : "books"
  USERS ||--o{ DEVICE_SESSIONS : "uses"
  USERS ||--o{ SAVED_LOCATIONS : "saves"
  DRIVER_PROFILES ||--o{ DRIVER_DOCUMENTS : "uploads"
  DRIVER_PROFILES ||--o| DRIVER_LOCATIONS : "has latest"
  DRIVER_PROFILES ||--o{ GPS_ANOMALIES : "triggers"
  DRIVER_PROFILES ||--o{ RIDES : "drives"
  RIDES ||--o{ RIDE_EVENTS : "records"
  RIDES ||--o{ NEGOTIATION_ROUNDS : "has"

  USERS {
    uuid id PK
    string phone_number
    string full_name
    string role_state
    string device_id
    string fcm_token
    boolean is_suspended
    timestamp suspension_until
  }

  DRIVER_PROFILES {
    uuid id PK
    uuid user_id FK
    string transport_type
    string vehicle_plate
    string license_number
    string approval_status
    boolean policy_accepted
    boolean is_online
    decimal acceptance_rate
    int total_rides
  }

  RIDES {
    uuid id PK
    uuid customer_id FK
    uuid driver_id FK
    string transport_type
    string status
    geography pickup_point
    geography destination_point
    decimal customer_initial_fare
    decimal agreed_fare
    timestamp fare_locked_at
    timestamp driver_arrived_at
    boolean pickup_expired
    timestamp started_at
    timestamp completed_at
  }

  NEGOTIATION_ROUNDS {
    uuid id PK
    uuid ride_id FK
    int round_number
    string proposed_by
    decimal proposed_amount
    string response
  }

  ROUTE_CACHE {
    uuid id PK
    string cache_key
    string origin_geohash
    string dest_geohash
    string vehicle_type
    float distance_km
    int duration_minutes
    json agreed_fares
    int avg_fare_rwf
  }
```

## 6. Customer Use Case Diagram

```mermaid
flowchart LR
  Customer["Customer"]
  Register["Register/Login"]
  ManageProfile["Manage Profile"]
  ManageLocations["Manage Saved Locations"]
  ViewNearby["View Nearby Drivers"]
  BookRide["Book Ride"]
  Negotiate["Negotiate Fare"]
  TrackRide["Track Ride"]
  CancelRide["Cancel Ride"]
  ViewHistory["View Ride History"]

  Customer --> Register
  Customer --> ManageProfile
  Customer --> ManageLocations
  Customer --> ViewNearby
  Customer --> BookRide
  Customer --> Negotiate
  Customer --> TrackRide
  Customer --> CancelRide
  Customer --> ViewHistory
```

## 7. Driver Use Case Diagram

```mermaid
flowchart LR
  Driver["Driver/Rider"]
  Apply["Apply as Driver"]
  UploadDocs["Upload Document URLs"]
  AcceptPolicy["Accept Policy"]
  GoOnline["Go Online"]
  SendLocation["Send Location"]
  AcceptRide["Accept Ride Request"]
  Negotiate["Negotiate/Lock Fare"]
  Navigate["Navigate Ride"]
  Arrive["Mark Arrived"]
  Start["Start Ride"]
  Complete["Complete Ride"]
  NoShowCancel["Cancel After Pickup Expiry"]
  ViewEarnings["View Earnings"]

  Driver --> Apply
  Driver --> UploadDocs
  Driver --> AcceptPolicy
  Driver --> GoOnline
  Driver --> SendLocation
  Driver --> AcceptRide
  Driver --> Negotiate
  Driver --> Navigate
  Driver --> Arrive
  Driver --> Start
  Driver --> Complete
  Driver --> NoShowCancel
  Driver --> ViewEarnings
```

## 8. Customer Ride Sequence

```mermaid
sequenceDiagram
  actor C as Customer App
  participant API as Go API
  participant R as Ride Service
  participant M as Matching Engine
  participant D as Driver WS
  participant N as Negotiation Service
  participant DB as Postgres
  participant Redis as Redis

  C->>API: POST /customer/rides
  API->>R: CreateRide()
  R->>DB: INSERT ride SEARCHING
  R->>Redis: set active ride/state
  R->>M: StartSearch()
  M->>Redis: GEOSEARCH drivers
  M->>D: ride_request
  D-->>M: accept
  M->>DB: assign driver, transition NEGOTIATING
  C->>API: propose/accept fare
  API->>N: Propose/Accept
  N->>DB: insert round / lock fare
  N-->>C: ride_confirmed WS
```

## 9. Driver Journey Activity

```mermaid
flowchart TD
  A["Open Driver App"] --> B["Apply / Profile Exists?"]
  B --> C["Upload Document URLs"]
  C --> D["Accept Policy"]
  D --> E["Admin Approves"]
  E --> F["Go Online"]
  F --> G["Send Location"]
  G --> H["Receive Ride Request"]
  H --> I{"Accept?"}
  I -- No --> J["Decline; possible penalty"]
  J --> F
  I -- Yes --> K["Negotiation"]
  K --> L["Fare Confirmed"]
  L --> M["Go En Route"]
  M --> N["Arrive at Pickup"]
  N --> O{"Customer Arrived?"}
  O -- No after expiry --> P["Cancel No-Show Without Penalty"]
  O -- Yes --> Q["Start Ride"]
  Q --> R["Complete Ride"]
  R --> S["Driver Released + Payout Updated"]
```

## 10. Ride State Diagram

```mermaid
stateDiagram-v2
  [*] --> SEARCHING
  SEARCHING --> MATCHED
  SEARCHING --> CANCELLED
  MATCHED --> NEGOTIATING
  MATCHED --> SEARCHING
  NEGOTIATING --> CONFIRMED
  NEGOTIATING --> SEARCHING
  NEGOTIATING --> CANCELLED
  CONFIRMED --> DRIVER_EN_ROUTE
  DRIVER_EN_ROUTE --> DRIVER_ARRIVED
  DRIVER_ARRIVED --> IN_PROGRESS
  DRIVER_ARRIVED --> CANCELLED
  IN_PROGRESS --> COMPLETED
  COMPLETED --> [*]
  CANCELLED --> [*]
```

## 11. Deployment Diagram

```mermaid
flowchart TB
  Internet["Internet / Mobile Networks"]
  LB["Load Balancer / Ingress"]
  API1["API Container 1"]
  API2["API Container 2"]
  PG[("Managed PostgreSQL + PostGIS")]
  Redis[("Managed Redis")]
  CDN[("Object Storage + CDN")]
  Logs["Logs / Metrics / Alerts"]
  Providers["AT / Firebase / Payments"]

  Internet --> LB
  LB --> API1
  LB --> API2
  API1 --> PG
  API2 --> PG
  API1 --> Redis
  API2 --> Redis
  API1 --> CDN
  API2 --> CDN
  API1 --> Providers
  API2 --> Providers
  API1 --> Logs
  API2 --> Logs
```
