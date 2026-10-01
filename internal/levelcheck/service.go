// Package levelcheck verifies reported water-level readings against
// registered measurement points, immutable calibration versions and manual
// staff-gauge readings before the level window and gate interlock may use
// them.
package levelcheck

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

// Config tunes how device readings are matched and compared with manual
// staff-gauge readings of the same period.
type Config struct {
	ToleranceM  float64
	MatchWindow time.Duration
}

func (c Config) withDefaults() Config {
	if c.ToleranceM <= 0 {
		c.ToleranceM = 0.05
	}
	if c.MatchWindow <= 0 {
		c.MatchWindow = 5 * time.Minute
	}
	return c
}

// Service keeps the verification registry. Calibrations and readings are
// append-only: recalibration never rewrites the version a historical reading
// was verified under.
type Service struct {
	mu           sync.RWMutex
	config       Config
	recorder     *journal.Recorder
	points       map[string]model.MeasurementPoint
	calibrations map[string][]model.CalibrationVersion
	manuals      map[string][]model.ManualGaugeReading
	readings     map[uuid.UUID]model.VerifiedReading
	sink         func(model.VerifiedReading)
	now          func() time.Time
}

func NewService(recorder *journal.Recorder, config Config, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		config: config.withDefaults(), recorder: recorder,
		points:       make(map[string]model.MeasurementPoint),
		calibrations: make(map[string][]model.CalibrationVersion),
		manuals:      make(map[string][]model.ManualGaugeReading),
		readings:     make(map[uuid.UUID]model.VerifiedReading),
		now:          now,
	}
}

// BindSink registers a consumer notified whenever a reading becomes trusted
// (used to feed the leveling window). Called outside the service lock.
func (s *Service) BindSink(sink func(model.VerifiedReading)) {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
}

func (s *Service) RegisterPoint(ctx context.Context, chamberID, pointID, label string) (model.MeasurementPoint, error) {
	point, err := model.NewMeasurementPoint(chamberID, pointID, label, s.now())
	if err != nil {
		return model.MeasurementPoint{}, err
	}
	s.mu.Lock()
	if _, exists := s.points[point.ID]; exists {
		s.mu.Unlock()
		return model.MeasurementPoint{}, fmt.Errorf("measurement point %s already registered", point.ID)
	}
	s.points[point.ID] = point
	s.mu.Unlock()
	if _, err := s.recorder.Record(ctx, chamberID, model.EventLevelPointRegistered, uuid.Nil, 0, point); err != nil {
		return model.MeasurementPoint{}, err
	}
	return point, nil
}

// IssueCalibration appends a new calibration version for the point. The
// previous version is marked superseded but kept intact, so readings verified
// under it remain valid historical basis for earlier transits.
func (s *Service) IssueCalibration(ctx context.Context, chamberID, pointID string, offsetM float64, issuedBy, note string) (model.CalibrationVersion, error) {
	s.mu.Lock()
	point, err := s.pointLocked(chamberID, pointID)
	if err != nil {
		s.mu.Unlock()
		return model.CalibrationVersion{}, err
	}
	versions := s.calibrations[pointID]
	now := s.now().UTC()
	calibration, err := model.NewCalibrationVersion(point, uint64(len(versions))+1, offsetM, issuedBy, note, s.now())
	if err != nil {
		s.mu.Unlock()
		return model.CalibrationVersion{}, err
	}
	if len(versions) > 0 {
		versions[len(versions)-1].SupersededAt = &now
	}
	s.calibrations[pointID] = append(versions, calibration)
	s.mu.Unlock()
	if _, err := s.recorder.Record(ctx, point.ChamberID, model.EventLevelCalibrationIssued, uuid.Nil, 0, calibration); err != nil {
		return model.CalibrationVersion{}, err
	}
	return calibration, nil
}

// RecordManual stores a staff-gauge reading and re-evaluates pending device
// readings of the same period against it.
func (s *Service) RecordManual(ctx context.Context, chamberID, pointID string, meters float64, observedAt time.Time, reader string) (model.ManualGaugeReading, error) {
	s.mu.Lock()
	point, err := s.pointLocked(chamberID, pointID)
	if err != nil {
		s.mu.Unlock()
		return model.ManualGaugeReading{}, err
	}
	manual, err := model.NewManualGaugeReading(point, meters, observedAt, reader, s.now())
	if err != nil {
		s.mu.Unlock()
		return model.ManualGaugeReading{}, err
	}
	s.manuals[pointID] = append(s.manuals[pointID], manual)
	changed := s.comparePendingLocked(pointID, manual)
	s.mu.Unlock()
	if _, err := s.recorder.Record(ctx, point.ChamberID, model.EventLevelManualRecorded, uuid.Nil, 0, manual); err != nil {
		return model.ManualGaugeReading{}, err
	}
	if err := s.publish(ctx, changed); err != nil {
		return model.ManualGaugeReading{}, err
	}
	return manual, nil
}

// IngestSample verifies one reported device reading: it is corrected with the
// calibration version effective at the observation time and compared with a
// same-period manual reading when one exists.
func (s *Service) IngestSample(ctx context.Context, chamberID, pointID string, cycleID uuid.UUID, generation uint64, rawMeters float64, observedAt time.Time) (model.VerifiedReading, error) {
	if observedAt.IsZero() {
		observedAt = s.now()
	}
	s.mu.Lock()
	point, err := s.pointLocked(chamberID, pointID)
	if err != nil {
		s.mu.Unlock()
		return model.VerifiedReading{}, err
	}
	calibration, ok := calibrationAt(s.calibrations[pointID], observedAt)
	if !ok {
		s.mu.Unlock()
		return model.VerifiedReading{}, fmt.Errorf("no calibration effective for point %s at %s", pointID, observedAt.UTC().Format(time.RFC3339))
	}
	reading, err := model.NewVerifiedReading(point, calibration, cycleID, generation, rawMeters, observedAt, s.now())
	if err != nil {
		s.mu.Unlock()
		return model.VerifiedReading{}, err
	}
	if manual, found := nearestManual(s.manuals[pointID], reading.ObservedAt, s.config.MatchWindow); found {
		reading.CompareWith(manual, s.config.ToleranceM, s.now())
	}
	s.readings[reading.ID] = reading
	s.mu.Unlock()
	if err := s.publish(ctx, []model.VerifiedReading{reading}); err != nil {
		return model.VerifiedReading{}, err
	}
	return reading, nil
}

// Review applies an operator verdict to a reading that automation could not
// settle (or that drifted after the fact).
func (s *Service) Review(ctx context.Context, readingID uuid.UUID, decision model.ReadingStatus, reviewer, note string) (model.VerifiedReading, error) {
	s.mu.Lock()
	reading, ok := s.readings[readingID]
	if !ok {
		s.mu.Unlock()
		return model.VerifiedReading{}, fmt.Errorf("reading %s not found", readingID)
	}
	if err := reading.Review(decision, reviewer, note, s.now()); err != nil {
		s.mu.Unlock()
		return model.VerifiedReading{}, err
	}
	s.readings[readingID] = reading
	s.mu.Unlock()
	if err := s.publish(ctx, []model.VerifiedReading{reading}); err != nil {
		return model.VerifiedReading{}, err
	}
	return reading, nil
}

// LatestTrusted returns the most recently observed trusted reading for a
// chamber; it is the only level input the gate interlock may consume.
func (s *Service) LatestTrusted(chamberID string) (model.VerifiedReading, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest model.VerifiedReading
	found := false
	for _, reading := range s.readings {
		if reading.ChamberID != chamberID || reading.Status != model.ReadingTrusted {
			continue
		}
		if !found || reading.ObservedAt.After(latest.ObservedAt) {
			latest, found = reading, true
		}
	}
	return latest, found
}

func (s *Service) Points(chamberID string) []model.MeasurementPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.MeasurementPoint, 0, len(s.points))
	for _, point := range s.points {
		if point.ChamberID == chamberID {
			result = append(result, point)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// CurrentCalibration returns the calibration version effective now; the full
// history stays available through Calibrations.
func (s *Service) CurrentCalibration(pointID string) (model.CalibrationVersion, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return calibrationAt(s.calibrations[pointID], s.now())
}

func (s *Service) Calibrations(pointID string) []model.CalibrationVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.CalibrationVersion(nil), s.calibrations[pointID]...)
}

func (s *Service) Readings(chamberID string, status model.ReadingStatus) []model.VerifiedReading {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]model.VerifiedReading, 0, len(s.readings))
	for _, reading := range s.readings {
		if reading.ChamberID != chamberID {
			continue
		}
		if status != "" && reading.Status != status {
			continue
		}
		result = append(result, reading)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ObservedAt.Equal(result[j].ObservedAt) {
			return result[i].ID.String() < result[j].ID.String()
		}
		return result[i].ObservedAt.Before(result[j].ObservedAt)
	})
	return result
}

// pointLocked resolves a point and pins it to the chamber in the request, so
// readings can never be filed under a different chamber's URL. Caller holds
// the lock.
func (s *Service) pointLocked(chamberID, pointID string) (model.MeasurementPoint, error) {
	point, ok := s.points[pointID]
	if !ok {
		return model.MeasurementPoint{}, fmt.Errorf("measurement point %s is not registered", pointID)
	}
	if point.ChamberID != chamberID {
		return model.MeasurementPoint{}, fmt.Errorf("measurement point %s does not belong to chamber %s", pointID, chamberID)
	}
	return point, nil
}

// comparePendingLocked compares pending readings within the manual reading's
// period and returns every reading whose verification record changed. Caller
// holds the lock.
func (s *Service) comparePendingLocked(pointID string, manual model.ManualGaugeReading) []model.VerifiedReading {
	changed := make([]model.VerifiedReading, 0, 2)
	for id, reading := range s.readings {
		if reading.PointID != pointID || reading.Status != model.ReadingPending {
			continue
		}
		if !withinWindow(reading.ObservedAt, manual.ObservedAt, s.config.MatchWindow) {
			continue
		}
		reading.CompareWith(manual, s.config.ToleranceM, s.now())
		s.readings[id] = reading
		changed = append(changed, reading)
	}
	return changed
}

// publish records verification events for readings changed as a side effect
// and notifies the trusted-reading sink. Caller must not hold the lock.
func (s *Service) publish(ctx context.Context, readings []model.VerifiedReading) error {
	for _, reading := range readings {
		if _, err := s.recorder.Record(ctx, reading.ChamberID, model.EventLevelReadingVerified, reading.CycleID, reading.Generation, reading); err != nil {
			return err
		}
	}
	s.mu.RLock()
	sink := s.sink
	s.mu.RUnlock()
	if sink == nil {
		return nil
	}
	for _, reading := range readings {
		if reading.Status == model.ReadingTrusted {
			sink(reading)
		}
	}
	return nil
}

// calibrationAt resolves the version effective at the given time: the latest
// version whose EffectiveAt is not after t. Older versions remain addressable
// here, which is what keeps post-maintenance recalibration from rewriting the
// basis of earlier transits.
func calibrationAt(versions []model.CalibrationVersion, t time.Time) (model.CalibrationVersion, bool) {
	var selected model.CalibrationVersion
	found := false
	for _, version := range versions {
		if version.EffectiveAt.After(t.UTC()) {
			continue
		}
		newer := version.EffectiveAt.After(selected.EffectiveAt) ||
			(version.EffectiveAt.Equal(selected.EffectiveAt) && version.Version > selected.Version)
		if !found || newer {
			selected, found = version, true
		}
	}
	return selected, found
}

func nearestManual(manuals []model.ManualGaugeReading, observedAt time.Time, window time.Duration) (model.ManualGaugeReading, bool) {
	var nearest model.ManualGaugeReading
	found := false
	for _, manual := range manuals {
		if !withinWindow(manual.ObservedAt, observedAt, window) {
			continue
		}
		if !found || absDuration(manual.ObservedAt.Sub(observedAt)) < absDuration(nearest.ObservedAt.Sub(observedAt)) {
			nearest, found = manual, true
		}
	}
	return nearest, found
}

func withinWindow(a, b time.Time, window time.Duration) bool {
	return absDuration(a.Sub(b)) <= window
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

var errRestorePayload = errors.New("level event payload is invalid")

// Restore rebuilds registry state from journaled events during replay. It
// never records new events and never invokes the sink.
func (s *Service) Restore(event model.Event) error {
	switch event.Type {
	case model.EventLevelPointRegistered:
		var point model.MeasurementPoint
		if err := event.Decode(&point); err != nil {
			return err
		}
		s.mu.Lock()
		s.points[point.ID] = point
		s.mu.Unlock()
	case model.EventLevelCalibrationIssued:
		var calibration model.CalibrationVersion
		if err := event.Decode(&calibration); err != nil {
			return err
		}
		s.mu.Lock()
		versions := s.calibrations[calibration.PointID]
		if len(versions) > 0 && calibration.SupersededAt == nil {
			supersededAt := calibration.EffectiveAt
			versions[len(versions)-1].SupersededAt = &supersededAt
		}
		s.calibrations[calibration.PointID] = append(versions, calibration)
		s.mu.Unlock()
	case model.EventLevelManualRecorded:
		var manual model.ManualGaugeReading
		if err := event.Decode(&manual); err != nil {
			return err
		}
		s.mu.Lock()
		s.manuals[manual.PointID] = append(s.manuals[manual.PointID], manual)
		s.mu.Unlock()
	case model.EventLevelReadingVerified:
		var reading model.VerifiedReading
		if err := event.Decode(&reading); err != nil {
			return err
		}
		if reading.ID == uuid.Nil || !reading.Status.Valid() {
			return errRestorePayload
		}
		s.mu.Lock()
		s.readings[reading.ID] = reading
		s.mu.Unlock()
	default:
		return fmt.Errorf("unsupported level event %s", event.Type)
	}
	return nil
}
