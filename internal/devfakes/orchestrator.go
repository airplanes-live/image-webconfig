package devfakes

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// Orchestrator outcomes the devserver can simulate. The fake mirrors
// the real orchestrator's state-file envelope and failure encoding:
// a failed run keeps the failing step's name in `step` and carries the
// failure in `status` — there is no terminal `step` value for failures.
const (
	OrchestratorOutcomeOK          = "ok"
	OrchestratorOutcomeFailApt     = "fail-apt"
	OrchestratorOutcomeFailRuntime = "fail-runtime"
)

// orchestratorStepDelay paces the simulated state-file progression.
// Long enough that the SPA's 2 s poll observes the intermediate steps,
// short enough that a dev iteration isn't a coffee break.
const orchestratorStepDelay = 3 * time.Second

// orchestratorState is the on-disk envelope the real orchestrator
// writes to /run/airplanes/orchestrator.state.
type orchestratorState struct {
	Step            string  `json:"step"`
	Status          string  `json:"status"`
	StartedAt       *string `json:"started_at"`
	FinishedAt      *string `json:"finished_at"`
	Error           *string `json:"error"`
	AptIrreversible bool    `json:"apt_irreversible"`
}

// SetOrchestratorOutcome selects which ending the next simulated
// orchestrator run gets. Validates against the known outcomes so a
// typo'd devserver flag fails at startup instead of silently running
// the ok path.
func (s *State) SetOrchestratorOutcome(outcome string) error {
	switch outcome {
	case OrchestratorOutcomeOK, OrchestratorOutcomeFailApt, OrchestratorOutcomeFailRuntime:
	default:
		return fmt.Errorf("unknown orchestrator outcome %q (want %s|%s|%s)",
			outcome, OrchestratorOutcomeOK, OrchestratorOutcomeFailApt, OrchestratorOutcomeFailRuntime)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orchestratorOutcome = outcome
	return nil
}

// StartOrchestratorRun simulates the update orchestrator's state-file
// progression in the background, paced by stepDelay (0 → the devserver
// default). The sequence mirrors the real orchestrator: an entry write
// (idle/running), apt with apt_irreversible raised before the mutation
// window, then runtime, then a terminal write — `done/ok` on success,
// or `<step>/failed` + error per the configured outcome.
func (s *State) StartOrchestratorRun(stepDelay time.Duration) {
	if stepDelay <= 0 {
		stepDelay = orchestratorStepDelay
	}
	s.mu.Lock()
	outcome := s.orchestratorOutcome
	s.mu.Unlock()
	if outcome == "" {
		outcome = OrchestratorOutcomeOK
	}

	go func() {
		now := func() *string {
			v := time.Now().UTC().Format(time.RFC3339)
			return &v
		}
		write := func(st orchestratorState) {
			b, err := json.Marshal(st)
			if err != nil {
				log.Printf("devfakes: orchestrator state marshal: %v", err)
				return
			}
			if err := writeAtomic(s.Paths.OrchestratorState, b, 0o644); err != nil {
				log.Printf("devfakes: orchestrator state write: %v", err)
			}
		}
		errStr := func(msg string) *string { return &msg }

		write(orchestratorState{Step: "idle", Status: "running"})
		time.Sleep(stepDelay)

		started := now()
		write(orchestratorState{Step: "apt", Status: "running", StartedAt: started, AptIrreversible: true})
		time.Sleep(stepDelay)
		if outcome == OrchestratorOutcomeFailApt {
			write(orchestratorState{
				Step: "apt", Status: "failed", StartedAt: started, FinishedAt: now(),
				Error: errStr("step exited with code 100"), AptIrreversible: true,
			})
			return
		}
		write(orchestratorState{Step: "apt", Status: "ok", StartedAt: started, FinishedAt: now(), AptIrreversible: true})

		started = now()
		write(orchestratorState{Step: "runtime", Status: "running", StartedAt: started, AptIrreversible: true})
		time.Sleep(stepDelay)
		if outcome == OrchestratorOutcomeFailRuntime {
			write(orchestratorState{
				Step: "runtime", Status: "failed", StartedAt: started, FinishedAt: now(),
				Error: errStr("runtime self-update exited non-zero"), AptIrreversible: true,
			})
			return
		}
		write(orchestratorState{Step: "runtime", Status: "ok", StartedAt: started, FinishedAt: now(), AptIrreversible: true})

		write(orchestratorState{Step: "done", Status: "ok", FinishedAt: now(), AptIrreversible: true})
	}()
}
