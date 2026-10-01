package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MeasurementPoint is a registered water-level gauge location in a chamber.
type MeasurementPoint struct {
	ID           string    `json:"id"`
	ChamberID    string    `json:"chamber_id"`
	Label        string    `json:"label"`
	RegisteredAt time.Time `json:"registered_at"`
}

func NewMeasurementPoint(chamberID, pointID, label string, now time.Time) (MeasurementPoint, error) {
	if chamberID == "" || label == "" {
		return MeasurementPoint{}, errors.New("measurement point requires chamber and label")
	}
	if pointID == "" {
		pointID = uuid.NewString()
	}
	return MeasurementPoint{
		ID: pointID, ChamberID: chamberID, Label: label,
		RegisteredAt: now.UTC(),
	}, nil
}

// CalibrationVersion is one immutable device calibration issue. Versions are
// append-only per point: a post-maintenance recalibration supersedes the
// current pointer but never rewrites earlier versions, so historical readings
// keep the version they were verified against.
type CalibrationVersion struct {
	ID           uuid.UUID  `json:"id"`
	PointID      string     `json:"point_id"`
	ChamberID    string     `json:"chamber_id"`
	Version      uint64     `json:"version"`
	OffsetM      float64    `json:"offset_m"`
	IssuedBy     string     `json:"issued_by"`
	Note         string     `json:"note,omitempty"`
	EffectiveAt  time.Time  `json:"effective_at"`
	SupersededAt *time.Time `json:"superseded_at,omitempty"`
}

func NewCalibrationVersion(point MeasurementPoint, version uint64, offsetM float64, issuedBy, note string, now time.Time) (CalibrationVersion, error) {
	if point.ID == "" {
		return CalibrationVersion{}, errors.New("calibration requires a registered measurement point")
	}
	if version == 0 {
		return CalibrationVersion{}, errors.New("calibration version must be positive")
	}
	if issuedBy == "" {
		return CalibrationVersion{}, errors.New("calibration issuer is required")
	}
	return CalibrationVersion{
		ID: uuid.New(), PointID: point.ID, ChamberID: point.ChamberID,
		Version: version, OffsetM: offsetM, IssuedBy: issuedBy, Note: note,
		EffectiveAt: now.UTC(),
	}, nil
}

// ManualGaugeReading is a staff-gauge reading taken on site and reported by
// field personnel; it is the reference device readings are checked against.
type ManualGaugeReading struct {
	ID         uuid.UUID `json:"id"`
	PointID    string    `json:"point_id"`
	ChamberID  string    `json:"chamber_id"`
	Meters     float64   `json:"meters"`
	ObservedAt time.Time `json:"observed_at"`
	Reader     string    `json:"reader"`
	RecordedAt time.Time `json:"recorded_at"`
}

func NewManualGaugeReading(point MeasurementPoint, meters float64, observedAt time.Time, reader string, now time.Time) (ManualGaugeReading, error) {
	if point.ID == "" {
		return ManualGaugeReading{}, errors.New("manual reading requires a registered measurement point")
	}
	if reader == "" {
		return ManualGaugeReading{}, errors.New("manual reading requires a reader")
	}
	if observedAt.IsZero() {
		observedAt = now
	}
	return ManualGaugeReading{
		ID: uuid.New(), PointID: point.ID, ChamberID: point.ChamberID,
		Meters: meters, ObservedAt: observedAt.UTC(), Reader: reader,
		RecordedAt: now.UTC(),
	}, nil
}

// ReadingStatus is the verification verdict attached to a device reading.
type ReadingStatus string

const (
	ReadingPending  ReadingStatus = "pending_review"
	ReadingTrusted  ReadingStatus = "trusted"
	ReadingRejected ReadingStatus = "rejected"
)

func (s ReadingStatus) Valid() bool {
	return s == ReadingPending || s == ReadingTrusted || s == ReadingRejected
}

// VerifiedReading is a device sample after calibration correction and
// comparison against the same-period manual reading. It carries the
// calibration version it was verified under; downstream consumers (level
// window, gate interlock) must only use readings in ReadingTrusted state.
type VerifiedReading struct {
	ID              uuid.UUID     `json:"id"`
	PointID         string        `json:"point_id"`
	ChamberID       string        `json:"chamber_id"`
	CycleID         uuid.UUID     `json:"cycle_id,omitempty"`
	Generation      uint64        `json:"generation,omitempty"`
	RawMeters       float64       `json:"raw_meters"`
	CorrectedMeters float64       `json:"corrected_meters"`
	ManualMeters    *float64      `json:"manual_meters,omitempty"`
	DeviationM      *float64      `json:"deviation_m,omitempty"`
	Status          ReadingStatus `json:"status"`
	CalibrationID   uuid.UUID     `json:"calibration_id"`
	Version         uint64        `json:"calibration_version"`
	ObservedAt      time.Time     `json:"observed_at"`
	DecidedAt       time.Time     `json:"decided_at"`
	DecidedBy       string        `json:"decided_by"`
	ReviewNote      string        `json:"review_note,omitempty"`
}

// NewVerifiedReading corrects a raw device sample with the calibration
// effective at the observation time. The reading starts pending until a
// manual comparison or an operator review marks it trusted.
func NewVerifiedReading(point MeasurementPoint, calibration CalibrationVersion, cycleID uuid.UUID, generation uint64, rawMeters float64, observedAt, now time.Time) (VerifiedReading, error) {
	if point.ID == "" {
		return VerifiedReading{}, errors.New("device reading requires a registered measurement point")
	}
	if calibration.PointID != point.ID || calibration.Version == 0 {
		return VerifiedReading{}, fmt.Errorf("no calibration effective for point %s", point.ID)
	}
	if observedAt.IsZero() {
		observedAt = now
	}
	return VerifiedReading{
		ID: uuid.New(), PointID: point.ID, ChamberID: point.ChamberID,
		CycleID: cycleID, Generation: generation,
		RawMeters: rawMeters, CorrectedMeters: rawMeters + calibration.OffsetM,
		Status: ReadingPending, CalibrationID: calibration.ID, Version: calibration.Version,
		ObservedAt: observedAt.UTC(), DecidedAt: now.UTC(), DecidedBy: "auto",
	}, nil
}

// CompareWith records the deviation against a manual reading of the same
// period and marks the reading trusted when within tolerance; otherwise it
// stays pending for operator review.
func (r *VerifiedReading) CompareWith(manual ManualGaugeReading, tolerance float64, now time.Time) {
	deviation := r.CorrectedMeters - manual.Meters
	if deviation < 0 {
		deviation = -deviation
	}
	r.ManualMeters = &manual.Meters
	r.DeviationM = &deviation
	r.DecidedAt = now.UTC()
	if deviation <= tolerance {
		r.Status = ReadingTrusted
		r.DecidedBy = "manual:" + manual.Reader
	}
}

// Review applies an operator verdict to a pending or previously decided
// reading. Reviews are journaled as new events; the calibration version the
// reading was verified under is never rewritten.
func (r *VerifiedReading) Review(decision ReadingStatus, reviewer, note string, now time.Time) error {
	if decision != ReadingTrusted && decision != ReadingRejected {
		return fmt.Errorf("review decision must be %s or %s", ReadingTrusted, ReadingRejected)
	}
	if reviewer == "" {
		return errors.New("reviewer is required")
	}
	r.Status = decision
	r.DecidedBy = reviewer
	r.ReviewNote = note
	r.DecidedAt = now.UTC()
	return nil
}
