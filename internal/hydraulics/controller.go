package hydraulics

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Controller coordinates valve commands, level windows and ramp operations.
type Controller struct {
	mu       sync.RWMutex
	commands *gate.Commands
	recorder *journal.Recorder
	windows  map[string]*LevelWindow
	ramp     *Ramp
	now      func() time.Time
}

func NewController(commands *gate.Commands, recorder *journal.Recorder, ramp *Ramp, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}
	return &Controller{
		commands: commands, recorder: recorder, windows: make(map[string]*LevelWindow),
		ramp: ramp, now: now,
	}
}

func (c *Controller) window(chamberID string) (*LevelWindow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	window := c.windows[chamberID]
	if window != nil {
		return window, nil
	}
	created, err := NewLevelWindow(4)
	if err != nil {
		return nil, err
	}
	c.windows[chamberID] = created
	return created, nil
}

func (c *Controller) BeginLeveling(cycle model.TransitCycle) error {
	window, err := c.window(cycle.ChamberID)
	if err != nil {
		return err
	}
	window.Begin(cycle.ID, cycle.Generation)
	return nil
}

// AcceptTrusted implements verification.TrustedReadingSink. Reports reach the
// level window only after they have been marked trusted together with the
// calibration version that produced them. A reading for a transit generation
// that no longer owns the window is refused; it stays on record as evidence
// but cannot move operations.
func (c *Controller) AcceptTrusted(reading model.VerifiedReading) error {
	window, err := c.windowForCycle(reading.CycleID, reading.Generation)
	if err != nil {
		return model.ErrReadingNotCurrentGeneration
	}
	return window.Add(reading.LevelSample())
}

// Observe feeds an already verified sample into the active window directly.
// Callers must go through the verification service; this entry point exists
// for recovery paths that already hold a trusted sample.
func (c *Controller) Observe(sample model.LevelSample) (bool, error) {
	window, err := c.windowForCycle(sample.CycleID, sample.Generation)
	if err != nil {
		return false, err
	}
	if err := window.Add(sample); err != nil {
		return false, err
	}
	return window.Stable(0.05), nil
}

func (c *Controller) windowForCycle(cycleID interface{ String() string }, generation uint64) (*LevelWindow, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, window := range c.windows {
		id, currentGeneration := window.Identity()
		if id.String() == cycleID.String() && currentGeneration == generation {
			return window, nil
		}
	}
	return nil, errors.New("no active level window for transit")
}

func (c *Controller) IssueValve(ctx context.Context, cycle model.TransitCycle, target float64) error {
	device := model.DeviceFillValve
	if target < 0 {
		device = model.DeviceDrainValve
		target = -target
	}
	command, err := c.commands.Issue(cycle, device, "set_opening", target, c.now())
	if err != nil {
		return err
	}
	_, err = c.recorder.Record(ctx, cycle.ChamberID, model.EventCommandQueued, cycle.ID, cycle.Generation, command)
	return err
}

func (c *Controller) RampTo(ctx context.Context, cycle model.TransitCycle, targets []float64) (RampResult, error) {
	return c.ramp.Run(ctx, cycle, targets, c.IssueValve)
}

func (c *Controller) WindowSamples(chamberID string) []model.LevelSample {
	c.mu.RLock()
	window := c.windows[chamberID]
	c.mu.RUnlock()
	if window == nil {
		return nil
	}
	return window.Samples()
}

// TrustedSample returns the most recent trusted window sample for a chamber.
// Interlocks consume this value rather than raw reports.
func (c *Controller) TrustedSample(chamberID string) (model.LevelSample, bool) {
	samples := c.WindowSamples(chamberID)
	if len(samples) == 0 {
		return model.LevelSample{}, false
	}
	return samples[len(samples)-1], true
}
