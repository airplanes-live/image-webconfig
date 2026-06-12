package devfakes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readOrchestratorState polls the backing file until it reaches the
// wanted terminal {step, status} or the deadline passes, returning the
// last observed state. The progression is asynchronous (goroutine with
// sleeps), so the test polls instead of asserting a fixed timeline.
func readOrchestratorState(t *testing.T, path, wantStep, wantStatus string) orchestratorState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last orchestratorState
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			var st orchestratorState
			if err := json.Unmarshal(body, &st); err != nil {
				t.Fatalf("orchestrator state parse: %v (body=%q)", err, body)
			}
			last = st
			if st.Step == wantStep && st.Status == wantStatus {
				return st
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("orchestrator state never reached %s/%s; last=%+v", wantStep, wantStatus, last)
	return last
}

func TestStartOrchestratorRun_OKEndsDone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewState(Paths{StateDir: dir, OrchestratorState: filepath.Join(dir, "orchestrator.state")})
	if err := s.SetOrchestratorOutcome(OrchestratorOutcomeOK); err != nil {
		t.Fatal(err)
	}
	s.StartOrchestratorRun(20 * time.Millisecond)
	st := readOrchestratorState(t, s.Paths.OrchestratorState, "done", "ok")
	if !st.AptIrreversible {
		t.Errorf("terminal done/ok: apt_irreversible = false, want true")
	}
	if st.Error != nil {
		t.Errorf("terminal done/ok: error = %q, want null", *st.Error)
	}
}

func TestStartOrchestratorRun_FailAptEndsAptFailed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewState(Paths{StateDir: dir, OrchestratorState: filepath.Join(dir, "orchestrator.state")})
	if err := s.SetOrchestratorOutcome(OrchestratorOutcomeFailApt); err != nil {
		t.Fatal(err)
	}
	s.StartOrchestratorRun(20 * time.Millisecond)
	st := readOrchestratorState(t, s.Paths.OrchestratorState, "apt", "failed")
	if !st.AptIrreversible {
		t.Errorf("terminal apt/failed: apt_irreversible = false, want true")
	}
	if st.Error == nil || *st.Error == "" {
		t.Errorf("terminal apt/failed: error missing")
	}
}

func TestSetOrchestratorOutcome_RejectsUnknown(t *testing.T) {
	t.Parallel()
	s := NewState(Paths{})
	if err := s.SetOrchestratorOutcome("fail-flash-capacitor"); err == nil {
		t.Fatal("unknown outcome accepted")
	}
}
