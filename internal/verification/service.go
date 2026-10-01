// Package verification owns the chain of evidence behind every level reading:
// registered measurement points, immutable device calibration versions,
// manual staff-gauge readings and the trusted / pending-review comparison.
// Only trusted readings (and the calibration version that produced them) are
// forwarded to the level windows and interlocks.
package verification

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// DefaultDeviationToleranceM is the maximum probe-vs-staff gap that still
// trusts a reading. The level window itself stabilises on 0.05 m; the manual
// cross-check is deliberately tighter.
const DefaultDeviationToleranceM = 0.02

// DefaultCompareWindow is how far apart a probe sample and a staff reading may
// be and still count as the same moment.
const DefaultCompareWindow = 10 * time.Minute

var (
	// ErrNotCurrentGeneration mirrors model.ErrReadingNotCurrentGeneration for
	// callers that check the verification package directly.
	ErrNotCurrentGeneration = model.ErrReadingNotCurrentGeneration
)

// TrustedReadingSink consumes readings that have earned operational trust.
type TrustedReadingSink interface {
	AcceptTrusted(reading model.VerifiedReading) error
}

// ReportedReading is a raw probe value waiting for verification.
type ReportedReading struct {
	ChamberID  string
	PointID    string
	DeviceID   string
	CycleID    uuid.UUID
	Generation uint64
	RawMeters  float64
	ObservedAt time.Time
}

// Outcome reports what happened to a reported reading.
type Outcome struct {
	Reading        model.VerifiedReading `json:"reading"`
	WindowAccepted bool                  `json:"window_accepted"`
	WindowNote     string                `json:"window_note,omitempty"`
}

// Config tunes the comparison rules.
type Config struct {
	DeviationToleranceM float64
	CompareWindow       time.Duration
}

func (c Config) withDefaults() Config {
	if c.DeviationToleranceM <= 0 {
		c.DeviationToleranceM = DefaultDeviationToleranceM
	}
	if c.CompareWindow <= 0 {
		c.CompareWindow = DefaultCompareWindow
	}
	return c
}

// Service keeps the verification register and emits append-only journal events.
type Service struct {
	mu            sync.RWMutex
	recorder      *journal.Recorder
	now           func() time.Time
	toleranceM    float64
	compareWindow time.Duration
	points        map[string]model.PointRegistration
	calibrations  map[string][]model.CalibrationRecord
	staff         map[string][]model.StaffReading
	readings      map[uuid.UUID]model.VerifiedReading
	readingOrder  []uuid.UUID
	sinks         map[string]TrustedReadingSink
}

func NewService(recorder *journal.Recorder, cfg Config, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	cfg = cfg.withDefaults()
	return &Service{
		recorder: recorder, now: now, toleranceM: cfg.DeviationToleranceM,
		compareWindow: cfg.CompareWindow,
		points:        make(map[string]model.PointRegistration),
		calibrations:  make(map[string][]model.CalibrationRecord),
		staff:         make(map[string][]model.StaffReading),
		readings:      make(map[uuid.UUID]model.VerifiedReading),
		sinks:         make(map[string]TrustedReadingSink),
	}
}

// BindSink connects the trusted-reading stream of a chamber to its window.
func (s *Service) BindSink(chamberID string, sink TrustedReadingSink) error {
	if chamberID == "" || sink == nil {
		return errors.New("sink binding requires chamber and sink")
	}
	s.mu.Lock()
	s.sinks[chamberID] = sink
	s.mu.Unlock()
	return nil
}

func pointKey(chamberID, pointID string) string   { return chamberID + "/" + pointID }
func deviceKey(chamberID, deviceID string) string { return chamberID + "/" + deviceID }

// RegisterPoint records a staff-gauge measurement point inside a chamber.
func (s *Service) RegisterPoint(ctx context.Context, input model.PointRegistration) (model.PointRegistration, error) {
	if input.ChamberID == "" || input.PointID == "" || input.DeviceID == "" {
		return model.PointRegistration{}, errors.New("point registration requires chamber, point and device")
	}
	input.RegisteredAt = s.now().UTC()
	if input.Label == "" {
		input.Label = input.PointID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pointKey(input.ChamberID, input.PointID)
	if _, exists := s.points[key]; exists {
		return model.PointRegistration{}, fmt.Errorf("measurement point %s already registered", key)
	}
	if _, err := s.recorder.Record(ctx, input.ChamberID, model.EventPointRegistered, uuid.Nil, 0, input); err != nil {
		return model.PointRegistration{}, err
	}
	s.points[key] = input
	return input, nil
}

// RegisterCalibration appends a new immutable calibration version for a probe.
// It never alters earlier versions or readings already verified against them.
func (s *Service) RegisterCalibration(ctx context.Context, chamberID, deviceID, version string, offsetM, scale float64, effectiveFrom time.Time, note string) (model.CalibrationRecord, error) {
	record, err := model.NewCalibrationRecord(chamberID, deviceID, version, offsetM, scale, effectiveFrom, s.now(), note)
	if err != nil {
		return model.CalibrationRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deviceKey(chamberID, deviceID)
	for _, existing := range s.calibrations[key] {
		if existing.Version == version {
			return model.CalibrationRecord{}, fmt.Errorf("calibration version %q already registered for %s", version, key)
		}
	}
	if _, err := s.recorder.Record(ctx, chamberID, model.EventCalibrationAdded, uuid.Nil, 0, record); err != nil {
		return model.CalibrationRecord{}, err
	}
	s.calibrations[key] = append(s.calibrations[key], record)
	return record, nil
}

// RecordStaff files one manual staff-gauge reading used to cross-check probes.
func (s *Service) RecordStaff(ctx context.Context, input model.StaffReading) (model.StaffReading, error) {
	reading, err := model.NewStaffReading(input.ChamberID, input.PointID, input.Meters, input.ObservedAt, input.Reader, input.Note)
	if err != nil {
		return model.StaffReading{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.points[pointKey(reading.ChamberID, reading.PointID)]; !ok {
		return model.StaffReading{}, fmt.Errorf("measurement point %s/%s is not registered", reading.ChamberID, reading.PointID)
	}
	if _, err := s.recorder.Record(ctx, reading.ChamberID, model.EventStaffRecorded, uuid.Nil, 0, reading); err != nil {
		return model.StaffReading{}, err
	}
	s.staff[pointKey(reading.ChamberID, reading.PointID)] = append(
		s.staff[pointKey(reading.ChamberID, reading.PointID)], reading)
	return reading, nil
}

// Verify compares a reported probe sample with the calibration in force at its
// observation time and the closest concurrent staff reading, then marks the
// result trusted or pending review.
func (s *Service) Verify(ctx context.Context, reported ReportedReading) (Outcome, error) {
	if reported.ChamberID == "" || reported.RawMeters < 0 {
		return Outcome{}, errors.New("reported reading requires a chamber and a non-negative value")
	}
	if reported.ObservedAt.IsZero() {
		reported.ObservedAt = s.now()
	}
	s.mu.Lock()
	point, ok := s.points[pointKey(reported.ChamberID, reported.PointID)]
	if !ok && reported.PointID == "" {
		point, ok = s.defaultPointLocked(reported.ChamberID)
	}
	if !ok {
		s.mu.Unlock()
		return Outcome{}, fmt.Errorf("measurement point %s/%s is not registered", reported.ChamberID, reported.PointID)
	}
	deviceID := reported.DeviceID
	if deviceID == "" {
		deviceID = point.DeviceID
	}
	if deviceID != point.DeviceID {
		s.mu.Unlock()
		return Outcome{}, fmt.Errorf("device %s is not bound to point %s", deviceID, point.PointID)
	}
	calibration, calOK := s.calibrationAtLocked(reported.ChamberID, deviceID, reported.ObservedAt)
	staff, staffOK := s.closestStaffLocked(point, reported.ObservedAt)

	reading := model.VerifiedReading{
		ID:         uuid.New(),
		ChamberID:  reported.ChamberID,
		PointID:    point.PointID,
		DeviceID:   deviceID,
		RawMeters:  reported.RawMeters,
		Meters:     reported.RawMeters,
		Trust:      model.TrustPending,
		CycleID:    reported.CycleID,
		Generation: reported.Generation,
		ObservedAt: reported.ObservedAt.UTC(),
		VerifiedAt: s.now().UTC(),
	}
	switch {
	case !calOK:
		reading.ReviewReason = "no calibration version in force at observation time"
	case !staffOK:
		reading.CalibrationID = calibration.ID
		reading.CalibrationVer = calibration.Version
		reading.Meters = calibration.Apply(reported.RawMeters)
		reading.ReviewReason = "no concurrent manual staff reading"
	default:
		reading.CalibrationID = calibration.ID
		reading.CalibrationVer = calibration.Version
		reading.Meters = calibration.Apply(reported.RawMeters)
		deviation := reading.Meters - staff.Meters
		reading.StaffID = &staff.ID
		reading.StaffMeters = &staff.Meters
		reading.DeviationM = deviation
		if absFloat(deviation) <= s.toleranceM {
			reading.Trust = model.TrustTrusted
		} else {
			reading.ReviewReason = fmt.Sprintf("deviation %.3f m exceeds tolerance %.3f m", deviation, s.toleranceM)
		}
	}
	if _, err := s.recorder.Record(ctx, reading.ChamberID, model.EventReadingVerified, reading.CycleID, reading.Generation, reading); err != nil {
		s.mu.Unlock()
		return Outcome{}, err
	}
	s.readings[reading.ID] = reading
	s.readingOrder = append(s.readingOrder, reading.ID)
	sink := s.sinks[reading.ChamberID]
	s.mu.Unlock()

	outcome := Outcome{Reading: reading}
	if reading.Trust == model.TrustTrusted && sink != nil {
		if err := sink.AcceptTrusted(reading); err != nil {
			if errors.Is(err, ErrNotCurrentGeneration) {
				outcome.WindowNote = err.Error()
			} else {
				return outcome, err
			}
		} else {
			outcome.WindowAccepted = true
		}
	}
	return outcome, nil
}

// Review lets an operator clear a pending reading after manual follow-up. A
// trusted reading is a historical basis and cannot be downgraded.
func (s *Service) Review(ctx context.Context, readingID uuid.UUID, trusted bool, reason string) (model.VerifiedReading, error) {
	s.mu.Lock()
	reading, ok := s.readings[readingID]
	if !ok {
		s.mu.Unlock()
		return model.VerifiedReading{}, fmt.Errorf("reading %s not found", readingID)
	}
	if reading.Trust == model.TrustTrusted {
		s.mu.Unlock()
		return model.VerifiedReading{}, fmt.Errorf("reading %s is already trusted and cannot be downgraded", readingID)
	}
	if !trusted {
		reading.ReviewReason = reason
	} else {
		reading.Trust = model.TrustTrusted
		reading.ReviewReason = reason
	}
	if _, err := s.recorder.Record(ctx, reading.ChamberID, model.EventReadingReviewed, reading.CycleID, reading.Generation, reading); err != nil {
		s.mu.Unlock()
		return model.VerifiedReading{}, err
	}
	s.readings[readingID] = reading
	sink := s.sinks[reading.ChamberID]
	s.mu.Unlock()

	if trusted && sink != nil {
		_ = sink.AcceptTrusted(reading)
	}
	return reading, nil
}

func (s *Service) defaultPointLocked(chamberID string) (model.PointRegistration, bool) {
	for _, point := range s.points {
		if point.ChamberID == chamberID {
			return point, true
		}
	}
	return model.PointRegistration{}, false
}

// calibrationAtLocked returns the newest calibration effective at the instant.
func (s *Service) calibrationAtLocked(chamberID, deviceID string, at time.Time) (model.CalibrationRecord, bool) {
	history := s.calibrations[deviceKey(chamberID, deviceID)]
	var active model.CalibrationRecord
	found := false
	for _, record := range history {
		if !record.EffectiveFrom.After(at) && (!found || record.EffectiveFrom.After(active.EffectiveFrom)) {
			active, found = record, true
		}
	}
	return active, found
}

func (s *Service) closestStaffLocked(point model.PointRegistration, at time.Time) (model.StaffReading, bool) {
	var closest model.StaffReading
	best := s.compareWindow + 1
	found := false
	for _, reading := range s.staff[pointKey(point.ChamberID, point.PointID)] {
		gap := reading.ObservedAt.Sub(at)
		if gap < 0 {
			gap = -gap
		}
		if gap <= s.compareWindow && (!found || gap < best) {
			closest, best, found = reading, gap, true
		}
	}
	return closest, found
}

// CurrentCalibration exposes the version in force for a probe at a given time.
func (s *Service) CurrentCalibration(chamberID, deviceID string, at time.Time) (model.CalibrationRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.calibrationAtLocked(chamberID, deviceID, at)
}

// Points lists registered measurement points, optionally for one chamber.
func (s *Service) Points(chamberID string) []model.PointRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.PointRegistration, 0, len(s.points))
	for _, point := range s.points {
		if chamberID == "" || point.ChamberID == chamberID {
			result = append(result, point)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ChamberID != result[j].ChamberID {
			return result[i].ChamberID < result[j].ChamberID
		}
		return result[i].PointID < result[j].PointID
	})
	return result
}

// Calibrations returns the append-only calibration history of a probe.
func (s *Service) Calibrations(chamberID, deviceID string) []model.CalibrationRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if chamberID == "" {
		var all []model.CalibrationRecord
		for _, history := range s.calibrations {
			all = append(all, history...)
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].ChamberID != all[j].ChamberID {
				return all[i].ChamberID < all[j].ChamberID
			}
			if all[i].DeviceID != all[j].DeviceID {
				return all[i].DeviceID < all[j].DeviceID
			}
			return all[i].EffectiveFrom.Before(all[j].EffectiveFrom)
		})
		return all
	}
	if deviceID != "" {
		return append([]model.CalibrationRecord(nil), s.calibrations[deviceKey(chamberID, deviceID)]...)
	}
	var all []model.CalibrationRecord
	for key, history := range s.calibrations {
		if chamber, _ := splitDeviceKey(key); chamber == chamberID {
			all = append(all, history...)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].DeviceID != all[j].DeviceID {
			return all[i].DeviceID < all[j].DeviceID
		}
		return all[i].EffectiveFrom.Before(all[j].EffectiveFrom)
	})
	return all
}

// StaffReadings lists manual readings for a point (or a whole chamber).
func (s *Service) StaffReadings(chamberID, pointID string) []model.StaffReading {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []model.StaffReading
	for key, history := range s.staff {
		chamber, point := splitPointKey(key)
		if chamberID != "" && chamber != chamberID {
			continue
		}
		if pointID != "" && point != pointID {
			continue
		}
		result = append(result, history...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ObservedAt.Before(result[j].ObservedAt) })
	return result
}

// Readings returns verification records in registration order.
func (s *Service) Readings(chamberID string, trust model.ReadingTrust) []model.VerifiedReading {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.VerifiedReading, 0, len(s.readingOrder))
	for _, id := range s.readingOrder {
		reading := s.readings[id]
		if chamberID != "" && reading.ChamberID != chamberID {
			continue
		}
		if trust != "" && reading.Trust != trust {
			continue
		}
		result = append(result, reading)
	}
	return result
}

// Reading fetches one verification record.
func (s *Service) Reading(id uuid.UUID) (model.VerifiedReading, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reading, ok := s.readings[id]
	return reading, ok
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// restorePoint applies a journaled registration without appending a new event.
func (s *Service) restorePoint(point model.PointRegistration) {
	s.mu.Lock()
	s.points[pointKey(point.ChamberID, point.PointID)] = point
	s.mu.Unlock()
}

// restoreCalibration appends a journaled version unless replay duplicated it.
func (s *Service) restoreCalibration(record model.CalibrationRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deviceKey(record.ChamberID, record.DeviceID)
	for _, existing := range s.calibrations[key] {
		if existing.ID == record.ID || existing.Version == record.Version {
			return
		}
	}
	s.calibrations[key] = append(s.calibrations[key], record)
}

// restoreStaff applies a journaled manual reading without re-recording it.
func (s *Service) restoreStaff(reading model.StaffReading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pointKey(reading.ChamberID, reading.PointID)
	for _, existing := range s.staff[key] {
		if existing.ID == reading.ID {
			return
		}
	}
	s.staff[key] = append(s.staff[key], reading)
}

// restoreReading applies a journaled verification (including review updates).
func (s *Service) restoreReading(reading model.VerifiedReading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.readings[reading.ID]; !exists {
		s.readingOrder = append(s.readingOrder, reading.ID)
	}
	s.readings[reading.ID] = reading
}

func splitPointKey(key string) (chamber, point string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func splitDeviceKey(key string) (chamber, device string) { return splitPointKey(key) }
