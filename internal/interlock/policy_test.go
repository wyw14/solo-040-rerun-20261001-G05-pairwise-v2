package interlock

import (
	"strings"
	"testing"

	"github.com/wyw14/cry-96/internal/model"
)

func closedGates() map[model.Direction]model.GatePosition {
	return map[model.Direction]model.GatePosition{
		model.DirectionUpstream:   model.GateClosed,
		model.DirectionDownstream: model.GateClosed,
	}
}

func TestValidateOpenRequiresTrustedLevel(t *testing.T) {
	policy := DefaultPolicy()
	snapshot := EquipmentSnapshot{
		ChamberID: "chamber-1", Gates: closedGates(),
		LevelM: 6.0, LevelTrusted: false,
	}
	err := policy.ValidateOpen(snapshot, model.DirectionUpstream)
	if err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Fatalf("untrusted level must block open, got %v", err)
	}
}

func TestValidateOpenAcceptsTrustedLevelInWindow(t *testing.T) {
	policy := DefaultPolicy()
	snapshot := EquipmentSnapshot{
		ChamberID: "chamber-1", Gates: closedGates(),
		LevelM: 6.0, LevelTrusted: true, LevelVersion: 3,
	}
	if err := policy.ValidateOpen(snapshot, model.DirectionUpstream); err != nil {
		t.Fatalf("trusted in-window level must allow open, got %v", err)
	}
}

func TestValidateOpenRejectsTrustedLevelOutsideWindow(t *testing.T) {
	policy := DefaultPolicy()
	snapshot := EquipmentSnapshot{
		ChamberID: "chamber-1", Gates: closedGates(),
		LevelM: 15.0, LevelTrusted: true, LevelVersion: 1,
	}
	if err := policy.ValidateOpen(snapshot, model.DirectionUpstream); err == nil {
		t.Fatal("out-of-window level must block open even when trusted")
	}
}
