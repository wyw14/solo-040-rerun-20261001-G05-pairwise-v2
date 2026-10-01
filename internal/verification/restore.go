package verification

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Restore rebuilds the verification register from the append-only journal.
// Calibration history is replayed as recorded, so a calibration registered
// after maintenance appends a new entry and never overwrites the version that
// backed earlier readings. Trusted readings are restored as evidence but are
// not re-pushed into level windows, which are per-process.
type Restore struct {
	store   journal.Store
	service *Service
}

func NewRestore(store journal.Store, service *Service) *Restore {
	return &Restore{store: store, service: service}
}

// RestoreAll replays every chamber partition.
func (r *Restore) RestoreAll(ctx context.Context) error {
	chambers, err := r.store.Chambers(ctx)
	if err != nil {
		return err
	}
	for _, chamberID := range chambers {
		if err := r.RestoreChamber(ctx, chamberID); err != nil {
			return err
		}
	}
	return nil
}

// RestoreChamber replays one chamber partition from the beginning.
func (r *Restore) RestoreChamber(ctx context.Context, chamberID string) error {
	events, err := r.store.Events(ctx, chamberID, 0)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.ChamberID != chamberID {
			return fmt.Errorf("verification replay partition mismatch chamber=%s event=%s", chamberID, event.ChamberID)
		}
		if err := r.apply(event); err != nil {
			return fmt.Errorf("verification replay event %d: %w", event.Sequence, err)
		}
	}
	return nil
}

func (r *Restore) apply(event model.Event) error {
	switch event.Type {
	case model.EventPointRegistered:
		var point model.PointRegistration
		if err := event.Decode(&point); err != nil {
			return err
		}
		r.service.restorePoint(point)
	case model.EventCalibrationAdded:
		var calibration model.CalibrationRecord
		if err := event.Decode(&calibration); err != nil {
			return err
		}
		r.service.restoreCalibration(calibration)
	case model.EventStaffRecorded:
		var staff model.StaffReading
		if err := event.Decode(&staff); err != nil {
			return err
		}
		r.service.restoreStaff(staff)
	case model.EventReadingVerified:
		var reading model.VerifiedReading
		if err := event.Decode(&reading); err != nil {
			return err
		}
		r.service.restoreReading(reading)
	case model.EventReadingReviewed:
		var reading model.VerifiedReading
		if err := event.Decode(&reading); err != nil {
			return err
		}
		r.service.restoreReading(reading)
	}
	return nil
}

// SeedReading looks up a restored reading by id; used by tests and diagnostics.
func (r *Restore) lookupReading(id uuid.UUID) (model.VerifiedReading, bool) {
	return r.service.Reading(id)
}
