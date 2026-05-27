# Taravelis Backend Documentation

This folder is the working documentation hub for the Taravelis ride-hailing backend. It is written for three audiences:

- Backend engineers implementing endpoints and business rules.
- DevOps engineers deploying and operating the API.
- Product/QA teammates validating customer, driver, and admin flows.

## Reading Order

| File | Purpose |
|---|---|
| [product/SRS.md](product/SRS.md) | Software requirements specification: scope, actors, functional and non-functional requirements. |
| [product/REQUIREMENTS.md](product/REQUIREMENTS.md) | Detailed requirement matrix and MVP acceptance checklist. |
| [architecture/SYSTEM_DESIGN.md](architecture/SYSTEM_DESIGN.md) | High-level architecture, runtime components, module boundaries, and request flow. |
| [architecture/DATA_MODEL.md](architecture/DATA_MODEL.md) | Database tables, relationships, Redis keys, and data ownership. |
| [architecture/ALGORITHMS.md](architecture/ALGORITHMS.md) | Matching, negotiation, geofence, GPS plausibility, payout, and route-cache algorithms. |
| [diagrams/README.md](diagrams/README.md) | Mermaid diagrams: context, containers, components, classes, ERD, use cases, sequence, and activity flows. |
| [backend/API_AND_PATTERNS.md](backend/API_AND_PATTERNS.md) | API conventions, response format, auth protection, role guards, and module patterns. |
| [backend/FILE_STRUCTURE.md](backend/FILE_STRUCTURE.md) | Codebase layout, module ownership, shared components, and naming conventions. |
| [devops/DEPLOYMENT.md](devops/DEPLOYMENT.md) | Local, staging, production deployment plan and operational checklist. |
| [devops/CI_CD.md](devops/CI_CD.md) | CI/CD pipeline design, checks, environments, rollback, and secrets. |
| [testing/TEST_STRATEGY.md](testing/TEST_STRATEGY.md) | Test suite strategy, coverage targets, and MVP verification flows. |
| [project/TASK_TRACKER.md](project/TASK_TRACKER.md) | Task tracker for backend, admin, DevOps, QA, and product work. |
| [MVP_LOCAL_DEVELOPMENT.md](MVP_LOCAL_DEVELOPMENT.md) | Local development runbook and MVP flow readiness. |

## Current System Summary

Taravelis is a Go modular monolith using:

- Go + Chi HTTP router.
- PostgreSQL + PostGIS for persistent relational and geospatial data.
- Redis for hot state, matching, sessions, route cache, and rate-limit state.
- WebSockets for real-time driver/customer ride updates.
- Africa's Talking integration points for OTP and masked calls.
- Firebase Cloud Messaging integration points for push notifications.
- MinIO in local development for document-storage/CDN simulation.

## API Version

Current API version: `v1`

Base path:

```text
/api/v1
```

Swagger:

```text
/swagger
/swagger/openapi.json
```

## Important MVP Decisions

- Customer policy acceptance is not implemented yet.
- Driver policy acceptance is one boolean, not five separate policy flags.
- Fare suggestion is not returned publicly. Fare and route data are collected for later analytics and future recommendation work.
- Generic/TBD destination rides can send final destination coordinates at completion.
- Driver payout shown in driver earnings is `agreed_fare * 0.85`.
- Driver can cancel after pickup expiry without a decline penalty.
- Admin review/document workflow is intentionally left for the admin workstream.
