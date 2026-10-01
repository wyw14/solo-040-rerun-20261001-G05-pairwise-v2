package interlock

import (
	"strings"
	"testing"

	"github.com/wyw14/cry-96/internal/model"
)

func gates() map[model.Direction]model.GatePosition {
	return map[model.Direction]model.GatePosition{
		model.DirectionUpstream:   model.GateClosed,
		model.DirectionDownstream: model.GateClosed,
	}
}

func TestValidateOpenRequiresTrustedReading(t *testing.T) {
	policy := DefaultPolicy()
	err := policy.ValidateOpen(EquipmentSnapshot{
		ChamberID: "chamber-1", Gates: gates(), LevelTrusted: false,
	}, model.DirectionUpstream)
	if err == nil || !strings.Contains(err.Error(), "trusted verified level") {
		t.Fatalf("open without trusted reading must be refused, got %v", err)
	}
}

func TestValidateOpenAcceptsTrustedReading(t *testing.T) {
	policy := DefaultPolicy()
	err := policy.ValidateOpen(EquipmentSnapshot{
		ChamberID: "chamber-1", Gates: gates(),
		LevelTrusted: true, LevelM: 6.0, CalibrationVersion: "cal-v1",
	}, model.DirectionUpstream)
	if err != nil {
		t.Fatalf("open with trusted in-range reading must pass, got %v", err)
	}
}

func TestValidateOpenRejectsOutOfRangeTrustedReading(t *testing.T) {
	policy := DefaultPolicy()
	err := policy.ValidateOpen(EquipmentSnapshot{
		ChamberID: "chamber-1", Gates: gates(),
		LevelTrusted: true, LevelM: 13.0, CalibrationVersion: "cal-v1",
	}, model.DirectionUpstream)
	if err == nil || !strings.Contains(err.Error(), "outside safe window") {
		t.Fatalf("out-of-range trusted reading must be rejected, got %v", err)
	}
}
