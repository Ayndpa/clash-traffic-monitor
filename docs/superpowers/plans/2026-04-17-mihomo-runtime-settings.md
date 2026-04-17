# Mihomo Runtime Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `mihomoUrl` and `secret` configurable from the web UI after startup while preserving Docker upgrade compatibility through environment-variable-first initialization and database persistence.

**Architecture:** Keep startup-only config limited to listener and storage concerns, and move Mihomo connection settings into a persisted application settings record inside the existing SQLite database. The service will load runtime Mihomo settings from environment variables when provided, persist them into SQLite, and otherwise fall back to saved settings. The frontend will block on a setup form when no Mihomo URL is configured and expose an edit action inside the dashboard for later changes.

**Tech Stack:** Go, net/http, SQLite, embedded HTML/CSS/JavaScript

---

### Task 1: Persist Mihomo runtime settings in SQLite

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

- [ ] **Step 1: Write the failing tests**
  Add tests that prove a new database can store and read Mihomo settings, and that environment variables override empty database state on startup.

- [ ] **Step 2: Run tests to verify they fail**
  Run: `go test ./...`
  Expected: failing assertions for missing settings persistence/runtime loading behavior.

- [ ] **Step 3: Write the minimal implementation**
  Add an `app_settings` table plus helpers to load/save Mihomo runtime settings, and initialize service runtime state with environment-first, database-second precedence.

- [ ] **Step 4: Run tests to verify they pass**
  Run: `go test ./...`
  Expected: the new settings tests pass without regressing existing tests.

### Task 2: Expose runtime settings APIs and safe collector behavior

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

- [ ] **Step 1: Write the failing tests**
  Add handler tests for `GET /api/settings/mihomo` and `PUT /api/settings/mihomo`, plus a behavior test showing the collector skips polling when the Mihomo URL is not configured.

- [ ] **Step 2: Run tests to verify they fail**
  Run: `go test ./...`
  Expected: route or assertion failures because the APIs and unconfigured behavior do not exist yet.

- [ ] **Step 3: Write the minimal implementation**
  Register settings routes, validate input, persist updates, refresh in-memory runtime settings immediately, and make polling return early when no Mihomo URL is configured.

- [ ] **Step 4: Run tests to verify they pass**
  Run: `go test ./...`
  Expected: settings API and collector behavior tests pass.

### Task 3: Add first-run setup and in-app editing UI

**Files:**
- Modify: `web/index.html`

- [ ] **Step 1: Define the frontend states**
  Add a blocking setup state for missing Mihomo URL, an edit entry point in the dashboard, and error/status messaging for save and load flows.

- [ ] **Step 2: Implement the minimal UI**
  Add a settings form for Mihomo URL and secret, fetch the current settings before loading traffic data, and only enter the dashboard after valid settings exist.

- [ ] **Step 3: Hook up runtime save flow**
  Submit settings through the new API, update local state, close the setup/edit surface on success, and refresh traffic data using the saved settings.

### Task 4: Update local-start documentation and verify end to end

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update local startup docs**
  Change the local run section to show `go build` then direct execution without mandatory `MIHOMO_URL`, and explain that the page will prompt for Mihomo URL and secret on first open if not already configured.

- [ ] **Step 2: Clarify Docker compatibility**
  Document that Docker users can still pass `MIHOMO_URL` and `MIHOMO_SECRET`, and those values take precedence at startup and are persisted for later restarts.

- [ ] **Step 3: Run full verification**
  Run: `go test ./...`
  Expected: all tests pass after the documentation and behavior changes.
