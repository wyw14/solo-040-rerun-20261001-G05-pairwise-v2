package interlock

import (
	"fmt"

	"github.com/wyw14/cry-96/internal/model"
)

type EquipmentSnapshot struct {
	ChamberID    string
	Gates        map[model.Direction]model.GatePosition
	LevelM       float64
	LevelTrusted bool
	LevelVersion uint64
	Emergency    bool
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
		return fmt.Errorf("no trusted water level reading available chamber=%s", snapshot.ChamberID)
	}
	if snapshot.LevelM < p.MinimumLevel || snapshot.LevelM > p.MaximumLevel {
		return fmt.Errorf("water level %.2f outside safe window", snapshot.LevelM)
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
