# ELD Fleet Management Backend
## Learning & Implementation Roadmap

**Tech stack:** Go backend  
**Target market:** USA  
**Approach:** Learn the domain gradually while implementing the project.

---

## Phase 1 — Basic Fleet SaaS

### Learn

- Motor carrier / company
- Company users
- User roles
  - Admin
  - Manager
  - Dispatcher
- Driver
- Truck / vehicle
- ELD device
- Company → users relationship
- Company → drivers relationship
- Company → trucks relationship
- Company → ELD devices relationship
- Truck → ELD relationship
- Active / inactive entities

### Implement

- Company CRUD
- User CRUD
- Driver CRUD
- Truck CRUD
- ELD CRUD
- Authentication
- Authorization
- Roles and permissions
- Multi-tenant/company data isolation

### Basic Domain

```text
Company
├── Users
├── Drivers
├── Trucks
└── ELD Devices
```

---

## Phase 2 — Subscription & Billing

### Learn

- Company subscription
- Subscription plans
- Pricing per truck / ELD / driver
- Billing cycle
- Trial period
- Subscription statuses
  - Active
  - Past due
  - Canceled
- Billable units
- Adding/removing billable units

### Implement

- Stripe integration
- Customer creation
- Subscription creation
- Subscription cancellation
- Subscription status synchronization
- Stripe webhooks
- Billable-unit tracking
- Billing access control

### Basic Flow

```text
Company
   ↓
Subscription
   ↓
Plan
   ↓
Billable Units
   ↓
Stripe
```

---

## Phase 3 — Fleet Operations

### Learn

- Driver → truck assignment
- ELD → truck assignment
- Driver switching trucks
- Co-driver concept
- Dispatcher → driver relationship
- Vehicle status
- ELD/device status
- Assignment history

### Implement

- Assign driver to truck
- Unassign driver
- Assign ELD to truck
- Replace ELD
- Driver/truck assignment history
- Current truck for driver
- Current driver(s) for truck
- Current ELD for truck

### Basic Domain

```text
Driver A ──┐
           ├── Truck 101 ─── ELD-123
Driver B ──┘
```

---

## Phase 4 — Basic ELD Communication

### Learn

- ELD device identification
- Device authentication
- ELD → backend communication
- Device heartbeat
- Last-seen time
- GPS/location
- Odometer
- Engine hours
- Engine on/off
- Vehicle moving/stopped
- Offline → online synchronization

### Implement

Build an ELD simulator instead of requiring physical hardware.

```text
ELD Simulator
      ↓
Backend API
      ↓
Truck / Device State
```

Store basic information such as:

```text
location
odometer
engine_hours
engine_on
moving
last_seen_at
```

---

## Phase 5 — Driver Logbook

### Learn

Four basic duty statuses:

- Off Duty
- Sleeper
- Driving
- On Duty

Also learn:

- Duty-status change
- Current duty status
- Status duration
- Daily driver log
- Log timeline
- Manual status changes
- Automatic driving status

### Implement

- Change driver status
- Get current status
- Store status history
- Generate daily timeline
- Calculate time spent in each status

Example:

```text
08:00  ON_DUTY
08:30  DRIVING
11:45  ON_DUTY
12:15  DRIVING
15:00  OFF_DUTY
```

---

## Phase 6 — Basic Hours of Service (HOS)

### Learn

- Driving hours
- On-duty hours
- Off-duty hours
- Driving limit
- Shift/window limit
- Required break
- Cycle limit
- Remaining available hours

Do not implement every exception yet.

### Implement

Calculate basic driver availability.

Example:

```json
{
  "driving_used": "07:32",
  "driving_remaining": "03:28",
  "shift_remaining": "04:15",
  "break_required": false
}
```

---

## Phase 7 — ELD Log Management

### Learn

- Editing logs
- Reason/annotation for edits
- Original records
- Corrected records
- Driver approval
- Driver certification
- Unidentified driving
- Assigning unidentified driving
- Audit history

### Important Concept

Treat ELD logs more like historical events than ordinary mutable CRUD records.

```text
Original Event
      ↓
Correction
      ↓
Current Version

Original history remains available.
```

### Implement

- Request log correction
- Store corrections
- Preserve original records
- Accept/reject corrections
- Certify daily logs
- Track unidentified driving
- Assign unidentified driving to drivers
- Audit trail

---

## Phase 8 — Compliance & Inspection

### Learn

- FMCSA
- ELD requirements
- RODS — Record of Duty Status
- Required ELD fields
- Daily log requirements
- Driver certification
- Roadside inspection
- ELD data transfer
- Record retention
- Time zones
- Required reports

### Implement

- Compliance-ready daily logs
- Driver log reports
- Inspection view
- Required ELD information
- Log export
- Data transfer preparation
- Historical record retrieval

---

## Phase 9 — Reliability & Real-World ELD Problems

### Learn

- ELD malfunction
- Diagnostic status
- Lost GPS
- Lost engine connection
- Device offline
- Delayed events
- Duplicate events
- Out-of-order events
- Offline mobile applications
- Synchronization
- Conflicts
- Device replacement

### Implement

- Idempotent event ingestion
- Event deduplication
- Offline event synchronization
- Out-of-order event handling
- Device heartbeat monitoring
- Device health
- Malfunction tracking
- Diagnostic tracking
- Reliable event processing

---

## Phase 10 — Advanced HOS & Production Compliance

### Learn

- Sleeper-berth rules
- Personal Conveyance
- Yard Move
- Adverse-driving conditions
- Short-haul exceptions
- HOS rule variations
- Cycle/restart rules
- HOS edge cases
- Log corrections affecting HOS
- Compliance violations
- Full ELD event model
- FMCSA certification/testing requirements

### Implement

- Advanced HOS engine
- HOS violations
- HOS exceptions
- Special driving categories
- Complete ELD event processing
- Compliance validation
- Production-level auditing
- FMCSA-compatible workflows

---

# Overall Roadmap

```text
Phase 1
Fleet CRUD
    ↓
Phase 2
Billing
    ↓
Phase 3
Fleet Assignments
    ↓
Phase 4
ELD → Backend Communication
    ↓
Phase 5
Driver Logbook
    ↓
Phase 6
Basic HOS
    ↓
Phase 7
Log Management
    ↓
Phase 8
Compliance & Inspection
    ↓
Phase 9
Reliability
    ↓
Phase 10
Advanced ELD / HOS
```

## Suggested Boundary

**Phases 1–3:** General SaaS and fleet-management backend.

**Phase 4:** Start learning how ELD hardware/device data interacts with your backend.

**Phase 5:** Start learning the actual ELD driver/logbook domain.

**Phase 6+:** Gradually introduce U.S. HOS regulations and compliance.

The goal is to avoid learning the entire ELD specification before writing code. Build the simple fleet system first, then introduce ELD-specific complexity only when the project requires it.
