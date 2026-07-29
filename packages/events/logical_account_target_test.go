package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

func TestLogicalAccountTargetRequestedRequiresTargetRunnerAndLogicalAccount(t *testing.T) {
	tests := map[string]func(*tradeeventpb.LogicalAccountTargetRequested){
		"target":          func(value *tradeeventpb.LogicalAccountTargetRequested) { value.TargetId = "" },
		"runner":          func(value *tradeeventpb.LogicalAccountTargetRequested) { value.RunnerId = "" },
		"logical account": func(value *tradeeventpb.LogicalAccountTargetRequested) { value.LogicalAccountId = "" },
		"sequence":        func(value *tradeeventpb.LogicalAccountTargetRequested) { value.CommandSequence = 0 },
	}
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validLogicalAccountTarget()
			mutate(value)
			if _, err := registry.Encode(
				LogicalAccountTargetRequested,
				value,
				validationOptions("target-1", "crypto", "logical-1"),
			); err == nil {
				t.Fatal("invalid logical account target was accepted")
			}
		})
	}
}

func TestLogicalAccountTargetRequestedAcceptsEmptyFullTarget(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	value := validLogicalAccountTarget()
	value.Targets = nil
	if _, err := registry.Encode(
		LogicalAccountTargetRequested,
		value,
		validationOptions("target-1", "crypto", "logical-1"),
	); err != nil {
		t.Fatalf("empty FULL target rejected: %v", err)
	}
}

func TestLogicalAccountTargetRequestedRejectsDuplicateInstrumentID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	value := validLogicalAccountTarget()
	value.Targets = append(value.Targets, &tradeeventpb.InstrumentTarget{
		InstrumentId: "BTC-USDT-SPOT",
		Quantity:     "2",
	})
	if _, err := registry.Encode(
		LogicalAccountTargetRequested,
		value,
		validationOptions("target-1", "crypto", "logical-1"),
	); err == nil {
		t.Fatal("duplicate instrument_id was accepted")
	}
}

func TestLogicalAccountTargetRequestedSubjectUsesLogicalAccountID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.Encode(
		LogicalAccountTargetRequested,
		validLogicalAccountTarget(),
		validationOptions("target-1", "crypto", "logical-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.RenderSubject(
		LogicalAccountTargetRequested,
		"crypto",
		"logical-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Subject != want {
		t.Fatalf("subject = %q, want %q", encoded.Subject, want)
	}
}

func validLogicalAccountTarget() *tradeeventpb.LogicalAccountTargetRequested {
	return &tradeeventpb.LogicalAccountTargetRequested{
		TargetId:         "target-1",
		RunnerId:         "runner-1",
		LogicalAccountId: "logical-1",
		CommandSequence:  1,
		Targets: []*tradeeventpb.InstrumentTarget{{
			InstrumentId: "BTC-USDT-SPOT",
			Quantity:     "1.25",
		}},
	}
}
