package provider

import (
	"encoding/json"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

func TestVerdictJSONFieldNames(t *testing.T) {
	raw, err := json.Marshal(Verdict{HardFail: true, Waived: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"drift":"","health":"","ackAllowed":false,"repairSameVersion":false,"hardFail":true,"waived":true}`; got != want {
		t.Fatalf("Verdict JSON = %s, want %s", got, want)
	}
}

// healthyActual is the acme/app 2.16.0 regression fixture: merged release PR,
// correct tag, complete public release, registries verified.
func healthyActual() Actual {
	return Actual{
		TagExists:          true,
		TagCommit:          "b6c2",
		ExpectedCommit:     "b6c2",
		ReleaseExists:      true,
		Latest:             true,
		AssetsComplete:     true,
		ChecksumsVerified:  true,
		RegistriesHealthy:  true,
		NoRegistryRequired: true,
	}
}

// caps fixture: a repository whose contract is "GitHub release + two asset files".
func caps() policy.Capabilities {
	return policy.Capabilities{GitHubRelease: true, Binaries: true, Checksums: true, Assets: []string{"app-linux", "app-macos"}}
}

func obs(state State) Observed {
	return Observed{Provider: KindReleasePlease, Version: "2.16.0", Actual: healthyActual(), ProviderState: state, Capabilities: caps()}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		observed   Observed
		wantDrift  Drift
		wantHealth domain.Health
		wantACK    bool
		wantHard   bool
		wantRepair bool
	}{
		{
			name:      "1 healthy release + pending provider => ACK missing",
			observed:  obs(StatePending),
			wantDrift: DriftACKMissing, wantHealth: domain.HealthACKPending, wantACK: true, wantRepair: true,
		},
		{
			name:      "2 healthy release + tagged provider => in sync noop",
			observed:  obs(StateTagged),
			wantDrift: DriftInSync, wantHealth: domain.HealthHealthy,
		},
		{
			name: "3 tag exists but release missing + pending => recoverable, no ACK",
			observed: func() Observed {
				o := obs(StatePending)
				o.Actual.ReleaseExists = false
				return o
			}(),
			wantDrift: DriftReleaseMissing, wantHealth: domain.HealthRecoverable, wantRepair: true,
		},
		{
			name: "4 release exists but assets missing + pending => recoverable",
			observed: func() Observed {
				o := obs(StatePending)
				o.Actual.AssetsComplete = false
				return o
			}(),
			wantDrift: DriftTransactionIncomplete, wantHealth: domain.HealthRecoverable, wantRepair: true,
		},
		{
			name: "5 registry published but GitHub release missing => same-version repair",
			observed: func() Observed {
				o := obs(StatePending)
				o.Capabilities.Registries = []string{"npm"}
				o.Capabilities.Registries = []string{"npm"}
				o.Actual.RegistriesHealthy = true
				o.Actual.ReleaseExists = false
				return o
			}(),
			wantDrift: DriftReleaseMissing, wantHealth: domain.HealthRecoverable, wantRepair: true,
		},
		{
			name: "6 provider tagged but release incomplete => false ACK degraded",
			observed: func() Observed {
				o := obs(StateTagged)
				o.Actual.AssetsComplete = false
				return o
			}(),
			wantDrift: DriftFalseACK, wantHealth: domain.HealthDegraded, wantRepair: true,
		},
		{
			name: "7 tag points to wrong commit => hard fail",
			observed: func() Observed {
				o := obs(StatePending)
				o.Actual.TagCommit = "dead"
				return o
			}(),
			wantDrift: DriftTagConflict, wantHealth: domain.HealthBroken, wantHard: true,
		},
		{
			name:      "8 provider state unreadable but release healthy => unknown, re-inspect then ACK",
			observed:  obs(StateUnknown),
			wantDrift: DriftStateUnknown, wantHealth: domain.HealthACKPending, wantRepair: true,
		},
		{
			name: "9 no tag yet => tag missing recoverable",
			observed: func() Observed {
				o := obs(StatePending)
				o.Actual.TagExists = false
				return o
			}(),
			wantDrift: DriftTagMissing, wantHealth: domain.HealthRecoverable, wantRepair: true,
		},
		{
			name: "10 required registry incomplete => recoverable",
			observed: func() Observed {
				o := obs(StatePending)
				o.Capabilities.Registries = []string{"npm"}
				o.Actual.RegistriesHealthy = false
				return o
			}(),
			wantDrift: DriftRegistryIncomplete, wantHealth: domain.HealthRecoverable, wantRepair: true,
		},
		{
			name: "11 manual provider has no ACK state; healthy => in sync",
			observed: func() Observed {
				o := obs(StateNone)
				o.Provider = KindManual
				return o
			}(),
			wantDrift: DriftInSync, wantHealth: domain.HealthHealthy,
		},
		{
			name: "12 latest flag wrong on otherwise complete release => transaction incomplete",
			observed: func() Observed {
				o := obs(StateTagged)
				o.Actual.Latest = false
				return o
			}(),
			wantDrift: DriftFalseACK, wantHealth: domain.HealthDegraded, wantRepair: true,
		},
		{
			name: "13 checksums unverified on pending => incomplete, never ACK",
			observed: func() Observed {
				o := obs(StatePending)
				o.Actual.ChecksumsVerified = false
				return o
			}(),
			wantDrift: DriftTransactionIncomplete, wantHealth: domain.HealthRecoverable, wantRepair: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.observed)
			if got.Drift != tc.wantDrift {
				t.Errorf("drift = %s, want %s", got.Drift, tc.wantDrift)
			}
			// Regression case TelePost 2.16.0: healthy actual, provider still pending.
			// ACK must be allowed and must never require a newer version.
			if got.Health != tc.wantHealth {
				t.Errorf("health = %s, want %s", got.Health, tc.wantHealth)
			}
			if got.ACKAllowed != tc.wantACK {
				t.Errorf("ackAllowed = %v, want %v", got.ACKAllowed, tc.wantACK)
			}
			if got.HardFail != tc.wantHard {
				t.Errorf("hardFail = %v, want %v", got.HardFail, tc.wantHard)
			}
			if got.RepairSameVersion != tc.wantRepair {
				t.Errorf("repairSameVersion = %v, want %v", got.RepairSameVersion, tc.wantRepair)
			}
		})
	}
}

func TestClassifyNeverACKsIncomplete(t *testing.T) {
	// Every incomplete actual state must deny ACK regardless of provider label.
	incomplete := []Actual{}
	base := healthyActual()
	for _, mutate := range []func(*Actual){
		func(a *Actual) { a.ReleaseExists = false },
		func(a *Actual) { a.AssetsComplete = false },
		func(a *Actual) { a.ChecksumsVerified = false },
		func(a *Actual) { a.Latest = false },
		func(a *Actual) { a.TagExists = false },
		func(a *Actual) { a.TagCommit = "other" },
	} {
		a := base
		mutate(&a)
		incomplete = append(incomplete, a)
	}
	for _, a := range incomplete {
		for _, state := range []State{StatePending, StateTagged} {
			v := Classify(Observed{Provider: KindReleasePlease, Actual: a, ProviderState: state, Capabilities: caps()})
			if v.ACKAllowed {
				t.Errorf("ACK allowed for incomplete release (state=%s drift=%s)", state, v.Drift)
			}
		}
	}
}

func TestPlanACKIdempotent(t *testing.T) {
	// First reconciliation removes pending and adds tagged.
	first := PlanACK([]string{"autorelease: pending"}, nil, nil)
	wantFirst := map[string]string{"autorelease: pending": "remove", "autorelease: tagged": "add"}
	if len(first) != 2 {
		t.Fatalf("first reconciliation: %d mutations, want 2 (%+v)", len(first), first)
	}
	for _, m := range first {
		if wantFirst[m.Label] != m.Action {
			t.Errorf("mutation %s action %s, want %s", m.Label, m.Action, wantFirst[m.Label])
		}
	}

	// Second reconciliation (tagged already present) is a NOOP — no duplicate mutation.
	second := PlanACK([]string{"autorelease: tagged"}, nil, nil)
	if len(second) != 0 {
		t.Fatalf("idempotent reconciliation produced mutations: %+v", second)
	}

	// pending + tagged together: corrects to tagged only.
	mixed := PlanACK([]string{"autorelease: pending", "autorelease: tagged"}, nil, nil)
	if len(mixed) != 1 || mixed[0] != (LabelMutation{Action: "remove", Label: "autorelease: pending"}) {
		t.Fatalf("mixed label reconciliation wrong: %+v", mixed)
	}

	// triggered label is also cleared.
	triggered := PlanACK([]string{"autorelease: triggered"}, nil, nil)
	if len(triggered) != 2 {
		t.Fatalf("triggered reconciliation wrong: %+v", triggered)
	}
}

func TestCanProgressToNextVersion(t *testing.T) {
	healthy := Classify(obs(StateTagged))
	ackMissing := Classify(obs(StatePending))
	conflict := Classify(func() Observed {
		o := obs(StatePending)
		o.Actual.TagCommit = "dead"
		return o
	}())

	if ok, why := CanProgressToNextVersion([]Verdict{healthy}); !ok {
		t.Errorf("healthy previous version blocked: %s", why)
	}
	if ok, _ := CanProgressToNextVersion([]Verdict{ackMissing}); ok {
		t.Error("ACK-missing previous version allowed next version")
	}
	if ok, why := CanProgressToNextVersion([]Verdict{conflict}); ok {
		t.Error("tag-conflicted previous version allowed next version")
	} else if why == "" {
		t.Error("conflict must give an explicit reason")
	}
	// Repaired previous version clears the gate.
	repaired := Classify(obs(StateTagged))
	if ok, _ := CanProgressToNextVersion([]Verdict{ackMissing, repaired}); ok {
		t.Error("any non-healthy prior version must block, not just the last")
	}
}

// A waived historical version is never healthy, but it must not block newer
// versions: the alternative is fabricating a release that never existed.
func TestWaiverAllowsProgressionWithoutFabricatingRelease(t *testing.T) {
	waived := obs(StateTagged)
	waived.Actual.ReleaseExists = false
	waived.Actual.Waived = true

	verdict := Classify(waived)
	if verdict.Drift != DriftHistoricalWaived || !verdict.Waived {
		t.Fatalf("drift = %s waived = %v, want HISTORICAL_WAIVED", verdict.Drift, verdict.Waived)
	}
	if verdict.Health == domain.HealthHealthy {
		t.Fatal("a waived version must never be reported HEALTHY")
	}
	if verdict.ACKAllowed {
		t.Fatal("a waived incomplete version must never be ACKed")
	}
	if verdict.RepairSameVersion {
		t.Fatal("a waived version must not be queued for repair")
	}
	if ok, why := CanProgressToNextVersion([]Verdict{verdict}); !ok {
		t.Fatalf("waived version blocked progression: %s", why)
	}
}
