# Task Tracker

Use this file as the shared project board until you move tasks into GitHub Issues, Linear, Jira, or Trello.

## Legend

| Status | Meaning |
|---|---|
| Todo | Not started. |
| Doing | In progress. |
| Review | Needs review/testing. |
| Done | Completed and verified. |

## DevOps Tasks

| ID | Task | Owner | Status |
|---|---|---|---|
| DEVOPS-001 | Add GitHub Actions CI for tests, Swagger, Docker build. | DevOps | Todo |
| DEVOPS-002 | Pick hosting provider for staging. | DevOps | Todo |
| DEVOPS-003 | Provision staging Postgres with PostGIS. | DevOps | Todo |
| DEVOPS-004 | Provision staging Redis. | DevOps | Todo |
| DEVOPS-005 | Provision object storage/CDN for driver documents. | DevOps | Todo |
| DEVOPS-006 | Configure secrets in deployment platform. | DevOps | Todo |
| DEVOPS-007 | Add production deployment runbook. | DevOps | Todo |
| DEVOPS-008 | Add log aggregation. | DevOps | Todo |
| DEVOPS-009 | Add metrics dashboard. | DevOps | Todo |
| DEVOPS-010 | Decide Swagger exposure policy for production. | DevOps | Todo |

## Backend Tasks

| ID | Task | Owner | Status |
|---|---|---|---|
| BE-001 | Add integration test harness with Postgres/Redis. | Backend | Todo |
| BE-002 | Add admin document review endpoints. | Backend | Todo |
| BE-003 | Add signed upload URL flow for document storage. | Backend | Todo |
| BE-004 | Add richer OpenAPI response schemas. | Backend | Todo |
| BE-005 | Add WebSocket cluster fanout or sticky-session deployment decision. | Backend/DevOps | Todo |
| BE-006 | Add customer policy acceptance when product is ready. | Backend | Deferred |
| BE-007 | Improve auth service tests. | Backend | Todo |
| BE-008 | Improve admin service tests. | Backend | Todo |

## Admin Workstream

| ID | Task | Owner | Status |
|---|---|---|---|
| ADM-001 | Build admin driver application detail endpoint. | Admin Backend | Todo |
| ADM-002 | Build document review approve/reject workflow. | Admin Backend | Todo |
| ADM-003 | Add admin action audit log. | Admin Backend | Todo |
| ADM-004 | Add filters for drivers, users, rides, anomalies. | Admin Backend | Todo |
| ADM-005 | Add system checks endpoint for DB, Redis, providers. | Admin Backend | Todo |
| ADM-006 | Add admin dashboard metrics endpoint. | Admin Backend | Todo |

## Mobile Coordination Tasks

| ID | Task | Owner | Status |
|---|---|---|---|
| MOB-001 | Implement customer Mapbox autocomplete. | Mobile | Todo |
| MOB-002 | Implement final destination submit on generic ride completion. | Mobile | Todo |
| MOB-003 | Implement driver document upload to object storage/CDN. | Mobile | Todo |
| MOB-004 | Implement driver ride request countdown UI. | Mobile | Todo |
| MOB-005 | Implement customer active ride WebSocket screen. | Mobile | Todo |
| MOB-006 | Implement driver no-show cancel after pickup expiry. | Mobile | Todo |

## QA Tasks

| ID | Task | Owner | Status |
|---|---|---|---|
| QA-001 | Write customer happy path test script. | QA | Todo |
| QA-002 | Write driver happy path test script. | QA | Todo |
| QA-003 | Write negotiation edge cases. | QA | Todo |
| QA-004 | Write pickup no-show edge cases. | QA | Todo |
| QA-005 | Verify Swagger matches API. | QA | Todo |

## Done Recently

| Task | Notes |
|---|---|
| Local runbook | Added MVP local development docs and Makefile. |
| Fare suggestion removal | Public route endpoints no longer return fare suggestions. |
| API version prefix | Centralized `apiV1Prefix`. |
| Manual fare lock | Added driver manual fare lock endpoint. |
| Driver arrived endpoint | Added driver arrival endpoint and timestamp migration. |
| No-show cancel | Added driver cancel after pickup expiry. |
| Route fare aggregation | Completion records agreed fare into route cache. |
| Driver payout | Earnings now apply 85% payout rate. |
| Local MinIO | Docker Compose includes MinIO for document-storage development. |
