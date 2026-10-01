package hydraulics

import (
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// LevelWindow owns a bounded sample sequence for one transit generation.
type LevelWindow struct {
	mu         sync.RWMutex
	cycleID    uuid.UUID
	generation uint64
	capacity   int
	samples    []model.LevelSample
}

func NewLevelWindow(capacity int) (*LevelWindow, error) {
	if capacity < 3 {
		return nil, errors.New("level window capacity must be at least three")
	}
	return &LevelWindow{capacity: capacity}, nil
}

func (w *LevelWindow) Begin(cycleID uuid.UUID, generation uint64) {
	w.mu.Lock()
	if w.cycleID != cycleID || w.generation != generation {
		w.cycleID = cycleID
		w.generation = generation
		w.samples = nil
	}
	w.mu.Unlock()
}

func (w *LevelWindow) Add(sample model.LevelSample) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sample.CycleID != w.cycleID || sample.Generation != w.generation {
		return errors.New("sample belongs to another transit generation")
	}
	if sample.CalibVer == "" {
		return errors.New("window accepts only verified readings with a calibration version")
	}
	w.samples = append(w.samples, sample)
	if len(w.samples) > w.capacity {
		w.samples = append([]model.LevelSample(nil), w.samples[len(w.samples)-w.capacity:]...)
	}
	return nil
}

func (w *LevelWindow) Stable(tolerance float64) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.samples) != w.capacity {
		return false
	}
	minimum, maximum := w.samples[0].Meters, w.samples[0].Meters
	for _, sample := range w.samples[1:] {
		if sample.Meters < minimum {
			minimum = sample.Meters
		}
		if sample.Meters > maximum {
			maximum = sample.Meters
		}
	}
	return maximum-minimum <= tolerance
}

func (w *LevelWindow) Samples() []model.LevelSample {
	w.mu.RLock()
	result := append([]model.LevelSample(nil), w.samples...)
	w.mu.RUnlock()
	return result
}

func (w *LevelWindow) Identity() (uuid.UUID, uint64) {
	w.mu.RLock()
	id, generation := w.cycleID, w.generation
	w.mu.RUnlock()
	return id, generation
}
