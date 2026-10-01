package interlock

import (
	"fmt"
	"time"

	"github.com/wyw14/cry-96/internal/model"
)

// EquipmentSnapshot carries only verified level evidence. LevelTrusted is false
// until the verification service has cleared a reading for the chamber, in
// which case opening a gate is refused rather than guessing a level.
type EquipmentSnapshot struct {
	ChamberID          string
	Gates              map[model.Direction]model.GatePosition
	LevelM             float64
	LevelTrusted       bool
	CalibrationVersion string
	LevelObservedAt    time.Time
	Emergency          bool
}

type Policy struct {
	MinimumLevel float64
	MaximumLevel float64
}

func DefaultPolicy() Policy { return Policy{MinimumLevel: 0, MaximumLevel: 12} }

func (p Policy) ValidateOpen(snapshot EquipmentSnapshot, direction model.Direction) error {
	if snapshot.Emergency {
		return fmt.Errorf("interlock emergency active chamber=%s", snapshot.ChamberID)
	}
	if !snapshot.LevelTrusted {
		return fmt.Errorf("no trusted verified level reading for chamber=%s", snapshot.ChamberID)
	}
	if snapshot.LevelM < p.MinimumLevel || snapshot.LevelM > p.MaximumLevel {
		return fmt.Errorf("water level %.2f (calibration %s) outside safe window", snapshot.LevelM, snapshot.CalibrationVersion)
	}
	opposite := snapshot.Gates[direction.Opposite()]
	if opposite != model.GateClosed {
		return fmt.Errorf("opposite gate is %s", opposite)
	}
	return nil
}

func (p Policy) ValidateClose(snapshot EquipmentSnapshot, direction model.Direction) error {
	position := snapshot.Gates[direction]
	if position == model.GateClosed || position == model.GateClosing {
		return fmt.Errorf("gate %s already closing or closed", direction)
	}
	return nil
}
