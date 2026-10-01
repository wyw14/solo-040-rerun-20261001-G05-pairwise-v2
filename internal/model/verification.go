package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrReadingNotCurrentGeneration is returned when a trusted reading arrives for
// a transit generation that does not own the active level window. The reading
// stays trusted on record but is not fed to the window.
var ErrReadingNotCurrentGeneration = errors.New("reading transit generation is not the active window")

// DeviceKind level probe additions are declared in device.go.

// ReadingTrust marks whether a reported level reading may feed operations.
type ReadingTrust string

const (
	// TrustPending is the initial state of every reported reading: recorded
	// but not yet cleared for the level window or interlocks.
	TrustPending ReadingTrust = "pending_review"
	// TrustTrusted readings are the only ones the window and interlocks consume.
	TrustTrusted ReadingTrust = "trusted"
)

func (t ReadingTrust) Valid() bool { return t == TrustPending || t == TrustTrusted }

// CalibrationRecord is one immutable calibration of a chamber level probe.
// Records are append-only: registering a new version after maintenance never
// mutates or deletes the version a past transit was evaluated against.
type CalibrationRecord struct {
	ID            uuid.UUID `json:"id"`
	ChamberID     string    `json:"chamber_id"`
	DeviceID      string    `json:"device_id"`
	Version       string    `json:"version"`
	OffsetM       float64   `json:"offset_m"`
	Scale         float64   `json:"scale"`
	EffectiveFrom time.Time `json:"effective_from"`
	RegisteredAt  time.Time `json:"registered_at"`
	Note          string    `json:"note,omitempty"`
}

// NewCalibrationRecord validates and stamps a calibration version.
func NewCalibrationRecord(chamberID, deviceID, version string, offsetM, scale float64, effectiveFrom, now time.Time, note string) (CalibrationRecord, error) {
	if chamberID == "" {
		return CalibrationRecord{}, errors.New("calibration requires a chamber")
	}
	if deviceID == "" {
		return CalibrationRecord{}, errors.New("calibration requires a device")
	}
	if version == "" {
		return CalibrationRecord{}, errors.New("calibration version is required")
	}
	if scale <= 0 {
		return CalibrationRecord{}, errors.New("calibration scale must be positive")
	}
	if effectiveFrom.IsZero() {
		effectiveFrom = now
	}
	return CalibrationRecord{
		ID: uuid.New(), ChamberID: chamberID, DeviceID: deviceID, Version: version,
		OffsetM: offsetM, Scale: scale, EffectiveFrom: effectiveFrom.UTC(),
		RegisteredAt: now.UTC(), Note: note,
	}, nil
}

// Apply converts a raw probe value to the calibrated meters used in comparison.
func (r CalibrationRecord) Apply(rawMeters float64) float64 {
	return rawMeters*r.Scale + r.OffsetM
}

// PointRegistration records a level measurement point (staff gauge location)
// inside a chamber where manual staff readings and the probe are compared.
type PointRegistration struct {
	ChamberID    string    `json:"chamber_id"`
	PointID      string    `json:"point_id"`
	Label        string    `json:"label"`
	DeviceID     string    `json:"device_id"`
	ZeroDatum    string    `json:"zero_datum,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

func NewPointRegistration(chamberID, pointID, label, deviceID, zeroDatum string, now time.Time) (PointRegistration, error) {
	if chamberID == "" || pointID == "" {
		return PointRegistration{}, errors.New("measurement point requires chamber and point ids")
	}
	if deviceID == "" {
		return PointRegistration{}, errors.New("measurement point requires a device")
	}
	if label == "" {
		label = pointID
	}
	return PointRegistration{
		ChamberID: chamberID, PointID: pointID, Label: label, DeviceID: deviceID,
		ZeroDatum: zeroDatum, RegisteredAt: now.UTC(),
	}, nil
}

// StaffReading is one manual staff-gauge reading taken at a measurement point.
type StaffReading struct {
	ID         uuid.UUID `json:"id"`
	ChamberID  string    `json:"chamber_id"`
	PointID    string    `json:"point_id"`
	Meters     float64   `json:"meters"`
	ObservedAt time.Time `json:"observed_at"`
	Reader     string    `json:"reader,omitempty"`
	Note       string    `json:"note,omitempty"`
}

func NewStaffReading(chamberID, pointID string, meters float64, observedAt time.Time, reader, note string) (StaffReading, error) {
	if chamberID == "" || pointID == "" {
		return StaffReading{}, errors.New("staff reading requires chamber and point")
	}
	if meters < 0 {
		return StaffReading{}, errors.New("staff reading cannot be negative")
	}
	if observedAt.IsZero() {
		return StaffReading{}, errors.New("staff reading requires an observation time")
	}
	return StaffReading{
		ID: uuid.New(), ChamberID: chamberID, PointID: pointID, Meters: meters,
		ObservedAt: observedAt.UTC(), Reader: reader, Note: note,
	}, nil
}

// VerifiedReading is the verification result for one reported probe sample.
// The raw sample, the calibration version applied to it, the staff reading it
// was compared against and the resulting trust mark travel together so the
// basis of every operational decision stays on record.
type VerifiedReading struct {
	ID             uuid.UUID    `json:"id"`
	ChamberID      string       `json:"chamber_id"`
	PointID        string       `json:"point_id"`
	DeviceID       string       `json:"device_id"`
	CalibrationID  uuid.UUID    `json:"calibration_id"`
	CalibrationVer string       `json:"calibration_version"`
	RawMeters      float64      `json:"raw_meters"`
	Meters         float64      `json:"meters"`
	StaffID        *uuid.UUID   `json:"staff_id,omitempty"`
	StaffMeters    *float64     `json:"staff_meters,omitempty"`
	DeviationM     float64      `json:"deviation_m,omitempty"`
	Trust          ReadingTrust `json:"trust"`
	CycleID        uuid.UUID    `json:"cycle_id,omitempty"`
	Generation     uint64       `json:"generation,omitempty"`
	ObservedAt     time.Time    `json:"observed_at"`
	VerifiedAt     time.Time    `json:"verified_at"`
	ReviewReason   string       `json:"review_reason,omitempty"`
}

// LevelSample extends the window sample with its measurement point and the
// calibration version that produced it. Only trusted readings reach windows.
func (r VerifiedReading) LevelSample() LevelSample {
	return LevelSample{
		CycleID:    r.CycleID,
		Generation: r.Generation,
		ChamberID:  r.ChamberID,
		PointID:    r.PointID,
		DeviceID:   r.DeviceID,
		CalibVer:   r.CalibrationVer,
		Meters:     r.Meters,
		ObservedAt: r.ObservedAt,
	}
}

// Key identifies a reported reading independent of its trust state.
func (r VerifiedReading) Key() string {
	return fmt.Sprintf("%s/%s/%d", r.ChamberID, r.PointID, r.ObservedAt.UnixNano())
}
