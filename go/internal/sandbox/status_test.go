// Tests for the JSON rendering of StatusReport. We construct the
// report directly rather than probing a real PG; the rendering layer
// is the only thing exercised here.

package sandbox

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/guriandoro/postgresql_sandbox/go/internal/config"
)

// runJSON renders a report and parses the result back into a generic
// map. Test helpers panic on render/parse failure so callers can
// focus on shape assertions.
func runJSON(t *testing.T, r *StatusReport) (map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := r.RenderJSON(&buf); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON parse: %v\n%s", err, buf.String())
	}
	return parsed, buf.String()
}

// mustHaveKeys asserts that every key in want is present in got.
// "Present" means the key exists; the value can be null.
func mustHaveKeys(t *testing.T, got map[string]any, want []string) {
	t.Helper()
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("JSON missing required key %q", k)
		}
	}
}

// mustNotHaveKeys asserts that none of the names in absent appear as
// top-level keys in got.
func mustNotHaveKeys(t *testing.T, got map[string]any, absent []string) {
	t.Helper()
	for _, k := range absent {
		if _, ok := got[k]; ok {
			t.Errorf("JSON unexpectedly has key %q", k)
		}
	}
}

func TestStatusRenderJSONRunningPrimaryWithReplicasAndPublications(t *testing.T) {
	rep := &StatusReport{
		Name:     "alpha",
		State:    RunStateRunning,
		Role:     config.RolePrimary,
		Host:     "127.0.0.1",
		Port:     55432,
		User:     "postgres",
		Database: "postgres",
		DataDir:  "/tmp/alpha/data",
		LogFile:  "/tmp/alpha/server.log",
		Version:  "PostgreSQL 18.0",
		Replicas: []ReplicaRow{
			{
				AppName:   "alpha_s1",
				State:     "streaming",
				SyncState: "async",
				WriteLag:  "00:00:00.012",
				FlushLag:  "00:00:00.018",
				ReplayLag: "00:00:00.020",
			},
		},
		Publications: []string{"pub_a"},
	}
	parsed, raw := runJSON(t, rep)

	mustHaveKeys(t, parsed, []string{
		"name", "state", "host", "port", "user", "database",
		"data_dir", "log_file", "role", "version",
		"replicas", "publications", "in_recovery",
	})
	mustNotHaveKeys(t, parsed, []string{"wal_receiver", "subscription"})

	if parsed["state"] != string(RunStateRunning) {
		t.Errorf("state = %v, want %q", parsed["state"], RunStateRunning)
	}
	if parsed["role"] != string(config.RolePrimary) {
		t.Errorf("role = %v, want %q", parsed["role"], config.RolePrimary)
	}
	if parsed["in_recovery"] != false {
		t.Errorf("in_recovery = %v, want false", parsed["in_recovery"])
	}

	reps, ok := parsed["replicas"].([]any)
	if !ok || len(reps) != 1 {
		t.Fatalf("replicas shape = %v (raw=%s)", parsed["replicas"], raw)
	}
	row, ok := reps[0].(map[string]any)
	if !ok {
		t.Fatalf("replicas[0] not an object: %v", reps[0])
	}
	for _, k := range []string{"app_name", "state", "sync_state", "write_lag", "flush_lag", "replay_lag"} {
		if _, ok := row[k]; !ok {
			t.Errorf("replicas[0] missing key %q", k)
		}
	}
	if row["app_name"] != "alpha_s1" {
		t.Errorf("replicas[0].app_name = %v, want alpha_s1", row["app_name"])
	}

	pubs, ok := parsed["publications"].([]any)
	if !ok || len(pubs) != 1 || pubs[0] != "pub_a" {
		t.Errorf("publications = %v, want [pub_a]", parsed["publications"])
	}
}

func TestStatusRenderJSONStoppedSandbox(t *testing.T) {
	rep := &StatusReport{
		Name:     "beta",
		State:    RunStateStopped,
		Role:     config.RoleUnknown,
		Host:     "127.0.0.1",
		Port:     55433,
		User:     "postgres",
		Database: "postgres",
		DataDir:  "/tmp/beta/data",
		LogFile:  "/tmp/beta/server.log",
		// Version, Replicas, WalReceiver, Publications, Subscription
		// all unset — the stopped path never runs the probes.
	}
	parsed, raw := runJSON(t, rep)

	mustHaveKeys(t, parsed, []string{
		"name", "state", "host", "port", "user", "database",
		"data_dir", "log_file", "replicas", "publications", "in_recovery",
	})
	// version is omitempty + empty → must be absent.
	// wal_receiver and subscription are omitempty + nil → must be absent.
	mustNotHaveKeys(t, parsed, []string{"version", "wal_receiver", "subscription"})

	// Replicas and Publications are nil — they must serialize to JSON
	// null so consumers can tell "probe didn't run" from "probe found
	// no rows". Both must still be present.
	if v, ok := parsed["replicas"]; !ok || v != nil {
		t.Errorf("replicas = %v (present=%v); want present and null. raw=%s", v, ok, raw)
	}
	if v, ok := parsed["publications"]; !ok || v != nil {
		t.Errorf("publications = %v (present=%v); want present and null. raw=%s", v, ok, raw)
	}

	if parsed["state"] != string(RunStateStopped) {
		t.Errorf("state = %v, want %q", parsed["state"], RunStateStopped)
	}
}

func TestStatusRenderJSONStandbyWithWalReceiver(t *testing.T) {
	rep := &StatusReport{
		Name:       "alpha_s1",
		State:      RunStateRunning,
		Role:       config.RoleStandby,
		Host:       "127.0.0.1",
		Port:       55434,
		User:       "postgres",
		Database:   "postgres",
		DataDir:    "/tmp/alpha_s1/data",
		LogFile:    "/tmp/alpha_s1/server.log",
		Version:    "PostgreSQL 18.0",
		InRecovery: true,
		WalReceiver: &WalReceiverRow{
			Status:          "streaming",
			ReceiveStartLSN: "0/3000000",
			WrittenLSN:      "0/3000148",
			FlushedLSN:      "0/3000148",
			LatestEndLSN:    "0/3000148",
		},
		// A standby running the full probe set still emits an empty
		// publications slice (probe ran, found nothing). Replicas is
		// nil because the primary-side query was skipped for this role.
		Publications: []string{},
	}
	parsed, raw := runJSON(t, rep)

	mustHaveKeys(t, parsed, []string{
		"name", "state", "host", "port", "user", "database",
		"data_dir", "log_file", "role", "version",
		"replicas", "publications", "in_recovery", "wal_receiver",
	})
	mustNotHaveKeys(t, parsed, []string{"subscription"})

	if parsed["in_recovery"] != true {
		t.Errorf("in_recovery = %v, want true", parsed["in_recovery"])
	}
	// Replicas nil → JSON null.
	if v, ok := parsed["replicas"]; !ok || v != nil {
		t.Errorf("replicas = %v (present=%v); want null. raw=%s", v, ok, raw)
	}
	// Publications empty slice → JSON [].
	pubs, ok := parsed["publications"].([]any)
	if !ok || pubs == nil {
		t.Errorf("publications = %v; want non-nil empty array. raw=%s", parsed["publications"], raw)
	}
	if len(pubs) != 0 {
		t.Errorf("publications len = %d, want 0", len(pubs))
	}

	wr, ok := parsed["wal_receiver"].(map[string]any)
	if !ok {
		t.Fatalf("wal_receiver shape = %v", parsed["wal_receiver"])
	}
	for _, k := range []string{"status", "receive_start_lsn", "written_lsn", "flushed_lsn", "latest_end_lsn"} {
		if _, ok := wr[k]; !ok {
			t.Errorf("wal_receiver missing key %q", k)
		}
	}
	if wr["status"] != "streaming" {
		t.Errorf("wal_receiver.status = %v, want streaming", wr["status"])
	}
}

func TestStatusRenderJSONLogicalSubscriber(t *testing.T) {
	rep := &StatusReport{
		Name:     "sub",
		State:    RunStateRunning,
		Role:     config.RoleSubscriber,
		Host:     "127.0.0.1",
		Port:     55435,
		User:     "postgres",
		Database: "postgres",
		DataDir:  "/tmp/sub/data",
		LogFile:  "/tmp/sub/server.log",
		Version:  "PostgreSQL 18.0",
		// Publications can be non-nil empty on a pure subscriber.
		Publications: []string{},
		Subscription: &SubscriptionRow{
			Name:            "sub_to_pub_a",
			Enabled:         true,
			WorkerPID:       "12345",
			ReceivedLSN:     "0/3000148",
			LatestEndLSN:    "0/3000148",
			LastMsgSendTime: "2026-06-05 10:00:00+00",
		},
	}
	parsed, _ := runJSON(t, rep)

	mustHaveKeys(t, parsed, []string{
		"name", "state", "host", "port", "user", "database",
		"data_dir", "log_file", "role", "version",
		"replicas", "publications", "in_recovery", "subscription",
	})
	mustNotHaveKeys(t, parsed, []string{"wal_receiver"})

	sub, ok := parsed["subscription"].(map[string]any)
	if !ok {
		t.Fatalf("subscription shape = %v", parsed["subscription"])
	}
	for _, k := range []string{"name", "enabled", "worker_pid", "received_lsn", "latest_end_lsn", "last_msg_send_time"} {
		if _, ok := sub[k]; !ok {
			t.Errorf("subscription missing key %q", k)
		}
	}
	if sub["name"] != "sub_to_pub_a" {
		t.Errorf("subscription.name = %v, want sub_to_pub_a", sub["name"])
	}
	if sub["enabled"] != true {
		t.Errorf("subscription.enabled = %v, want true", sub["enabled"])
	}
}

// TestStatusRenderJSONTrailingNewline locks the contract that the
// output ends with exactly one newline — same convention as
// GlobalStatus.RenderJSON and config show's JSON output, which
// scripts piping to `| jq` rely on.
func TestStatusRenderJSONTrailingNewline(t *testing.T) {
	rep := &StatusReport{
		Name:     "alpha",
		State:    RunStateStopped,
		Host:     "127.0.0.1",
		Port:     55432,
		User:     "postgres",
		Database: "postgres",
		DataDir:  "/tmp/alpha/data",
		LogFile:  "/tmp/alpha/server.log",
	}
	var buf bytes.Buffer
	if err := rep.RenderJSON(&buf); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	out := buf.String()
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Errorf("output should end with a newline; got %q", out)
	}
	// And no double newline.
	if len(out) >= 2 && out[len(out)-2] == '\n' {
		t.Errorf("output should not end with double newline; got %q", out)
	}
}
