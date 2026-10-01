package interlock

import (
	"context"
	"fmt"
	"time"

	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/model"
)

// Coordinator keeps policy evaluation and command reservation in one flow.
type Coordinator struct {
	policy    Policy
	arbiter   *Arbiter
	gates     *gate.Controller
	trusted   func(string) (model.LevelSample, bool)
	emergency func(string) bool
}

// NewCoordinator wires the interlock to a trusted-level provider. The provider
// must return only verified readings (see hydraulics.Controller.TrustedSample);
// raw reported levels never reach the interlock.
func NewCoordinator(policy Policy, arbiter *Arbiter, gates *gate.Controller, trustedLevel func(string) (model.LevelSample, bool), emergency func(string) bool) *Coordinator {
	return &Coordinator{policy: policy, arbiter: arbiter, gates: gates, trusted: trustedLevel, emergency: emergency}
}

func (c *Coordinator) Snapshot(chamberID string) EquipmentSnapshot {
	snapshot := EquipmentSnapshot{
		ChamberID: chamberID, Gates: c.gates.State().Snapshot(chamberID),
	}
	if c.trusted != nil {
		if sample, ok := c.trusted(chamberID); ok {
			snapshot.LevelM = sample.Meters
			snapshot.LevelTrusted = true
			snapshot.CalibrationVersion = sample.CalibVer
			snapshot.LevelObservedAt = sample.ObservedAt
		}
	}
	if c.emergency != nil {
		snapshot.Emergency = c.emergency(chamberID)
	}
	return snapshot
}

func (c *Coordinator) RequestOpen(ctx context.Context, cycle model.TransitCycle, direction model.Direction, owner string) (model.DeviceCommand, error) {
	if err := c.policy.ValidateOpen(c.Snapshot(cycle.ChamberID), direction); err != nil {
		return model.DeviceCommand{}, err
	}
	var command model.DeviceCommand
	_, err := c.arbiter.Reserve(cycle.ChamberID, direction, owner, func() error {
		issued, issueErr := c.gates.Request(ctx, cycle, direction, true)
		command = issued
		return issueErr
	})
	return command, err
}

func (c *Coordinator) RequestClose(ctx context.Context, cycle model.TransitCycle, direction model.Direction) (model.DeviceCommand, error) {
	if err := c.policy.ValidateClose(c.Snapshot(cycle.ChamberID), direction); err != nil {
		return model.DeviceCommand{}, err
	}
	return c.gates.Request(ctx, cycle, direction, false)
}

func (c *Coordinator) Release(chamberID, owner string) error {
	if !c.arbiter.Release(chamberID, owner) {
		return fmt.Errorf("reservation owner %q does not control chamber %s", owner, chamberID)
	}
	return nil
}

func (c *Coordinator) Reservation(chamberID string) (Reservation, bool) {
	return c.arbiter.Current(chamberID)
}

func (c *Coordinator) Observe(chamberID string, direction model.Direction, position model.GatePosition, _ time.Time) error {
	return c.gates.Observe(chamberID, direction, position)
}
