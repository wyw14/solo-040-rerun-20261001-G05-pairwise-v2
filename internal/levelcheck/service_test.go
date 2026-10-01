package levelcheck

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

func newTestService(now time.Time) (*Service, *journal.MemoryStore) {
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, func() time.Time { return now })
	service := NewService(recorder, Config{ToleranceM: 0.05, MatchWindow: 5 * time.Minute}, func() time.Time { return now })
	return service, store
}

func registerPointWithCalibration(t *testing.T, service *Service, offset float64) model.MeasurementPoint {
	t.Helper()
	point, err := service.RegisterPoint(context.Background(), "chamber-1", "pt-1", "upstream staff gauge")
	if err != nil {
		t.Fatalf("register point: %v", err)
	}
	if _, err := service.IssueCalibration(context.Background(), "chamber-1", point.ID, offset, "tech", "initial"); err != nil {
		t.Fatalf("issue calibration: %v", err)
	}
	return point
}

func TestCalibrationVersionsAreAppendOnly(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(now)
	point := registerPointWithCalibration(t, service, 0.02)

	first, err := service.IssueCalibration(context.Background(), "chamber-1", point.ID, 0.05, "tech", "post-maintenance")
	if err != nil {
		t.Fatalf("second calibration: %v", err)
	}
	if first.Version != 2 {
		t.Fatalf("expected version 2, got %d", first.Version)
	}
	history := service.Calibrations(point.ID)
	if len(history) != 2 {
		t.Fatalf("expected 2 retained versions, got %d", len(history))
	}
	if history[0].SupersededAt == nil {
		t.Fatal("old version should be marked superseded")
	}
	if history[0].OffsetM != 0.02 {
		t.Fatalf("old version offset rewritten: %v", history[0].OffsetM)
	}
	current, ok := service.CurrentCalibration(point.ID)
	if !ok || current.Version != 2 {
		t.Fatalf("current calibration = %v, %v", current, ok)
	}
}

func TestSampleStaysPendingUntilManualConfirms(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(now)
	point := registerPointWithCalibration(t, service, 0)

	reading, err := service.IngestSample(context.Background(), "chamber-1", point.ID, uuid.Nil, 0, 6.10, now)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if reading.Status != model.ReadingPending {
		t.Fatalf("unverified sample must be pending, got %s", reading.Status)
	}
	if _, ok := service.LatestTrusted("chamber-1"); ok {
		t.Fatal("no trusted reading may exist before manual confirmation")
	}

	if _, err := service.RecordManual(context.Background(), "chamber-1", point.ID, 6.12, now.Add(time.Minute), "crew"); err != nil {
		t.Fatalf("manual: %v", err)
	}
	stored := service.Readings("chamber-1", model.ReadingTrusted)
	if len(stored) != 1 {
		t.Fatalf("expected 1 trusted reading, got %d", len(stored))
	}
	if stored[0].DeviationM == nil || math.Abs(*stored[0].DeviationM-0.02) > 1e-9 {
		t.Fatalf("deviation = %v", stored[0].DeviationM)
	}
	latest, ok := service.LatestTrusted("chamber-1")
	if !ok || latest.CorrectedMeters != 6.10 {
		t.Fatalf("latest trusted = %+v, %v", latest, ok)
	}
}

func TestDeviationBeyondToleranceNeedsReview(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(now)
	point := registerPointWithCalibration(t, service, 0)

	if _, err := service.RecordManual(context.Background(), "chamber-1", point.ID, 6.00, now, "crew"); err != nil {
		t.Fatalf("manual: %v", err)
	}
	reading, err := service.IngestSample(context.Background(), "chamber-1", point.ID, uuid.Nil, 0, 6.40, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if reading.Status != model.ReadingPending {
		t.Fatalf("drifted sample must stay pending, got %s", reading.Status)
	}
	if reading.DeviationM == nil || math.Abs(*reading.DeviationM-0.40) > 1e-9 {
		t.Fatalf("deviation = %v", reading.DeviationM)
	}
	if _, ok := service.LatestTrusted("chamber-1"); ok {
		t.Fatal("drifted reading must not become trusted automatically")
	}

	reviewed, err := service.Review(context.Background(), reading.ID, model.ReadingRejected, "operator", "sensor drift confirmed")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if reviewed.Status != model.ReadingRejected || reviewed.DecidedBy != "operator" {
		t.Fatalf("reviewed = %+v", reviewed)
	}
	if _, ok := service.LatestTrusted("chamber-1"); ok {
		t.Fatal("rejected reading must not be trusted")
	}
}

func TestRecalibrationDoesNotRewriteHistory(t *testing.T) {
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(start)
	point := registerPointWithCalibration(t, service, 0.0)

	cycleID := uuid.New()
	old, err := service.IngestSample(context.Background(), "chamber-1", point.ID, cycleID, 3, 5.00, start)
	if err != nil {
		t.Fatalf("ingest old: %v", err)
	}
	if old.Version != 1 {
		t.Fatalf("old reading version = %d", old.Version)
	}

	// Maintenance recalibrates one hour later.
	later := start.Add(time.Hour)
	service.now = func() time.Time { return later }
	if _, err := service.IssueCalibration(context.Background(), "chamber-1", point.ID, 0.20, "tech", "post-maintenance"); err != nil {
		t.Fatalf("recalibration: %v", err)
	}

	// A sample observed before the recalibration still resolves version 1.
	late, err := service.IngestSample(context.Background(), "chamber-1", point.ID, cycleID, 3, 5.00, start.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("ingest late-arriving: %v", err)
	}
	if late.Version != 1 || late.CorrectedMeters != 5.00 {
		t.Fatalf("historical sample rebound to new calibration: %+v", late)
	}

	// A sample observed after the recalibration uses version 2.
	fresh, err := service.IngestSample(context.Background(), "chamber-1", point.ID, cycleID, 4, 5.00, later.Add(time.Minute))
	if err != nil {
		t.Fatalf("ingest fresh: %v", err)
	}
	if fresh.Version != 2 || fresh.CorrectedMeters != 5.20 {
		t.Fatalf("fresh sample = %+v", fresh)
	}

	// The old transit's readings still carry their original version.
	history := service.Readings("chamber-1", "")
	if history[0].Version != 1 || history[0].CalibrationID == fresh.CalibrationID {
		t.Fatalf("historical basis overwritten: %+v", history[0])
	}
}

func TestIngestRequiresEffectiveCalibration(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(now)
	point, err := service.RegisterPoint(context.Background(), "chamber-1", "pt-1", "gauge")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := service.IngestSample(context.Background(), "chamber-1", point.ID, uuid.Nil, 0, 6.0, now); err == nil {
		t.Fatal("sample without any calibration must be rejected")
	}
}

func TestCrossChamberAccessRejected(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(now)
	point := registerPointWithCalibration(t, service, 0)
	if _, err := service.IngestSample(context.Background(), "chamber-2", point.ID, uuid.Nil, 0, 6.0, now); err == nil {
		t.Fatal("sample via another chamber must be rejected")
	}
	if _, err := service.RecordManual(context.Background(), "chamber-2", point.ID, 6.0, now, "crew"); err == nil {
		t.Fatal("manual via another chamber must be rejected")
	}
}

func TestTrustedReadingsReachSink(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, _ := newTestService(now)
	point := registerPointWithCalibration(t, service, 0)
	var delivered []model.VerifiedReading
	service.BindSink(func(reading model.VerifiedReading) { delivered = append(delivered, reading) })

	if _, err := service.IngestSample(context.Background(), "chamber-1", point.ID, uuid.Nil, 0, 6.0, now); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(delivered) != 0 {
		t.Fatal("pending reading must not reach the sink")
	}
	if _, err := service.RecordManual(context.Background(), "chamber-1", point.ID, 6.01, now, "crew"); err != nil {
		t.Fatalf("manual: %v", err)
	}
	if len(delivered) != 1 || delivered[0].Status != model.ReadingTrusted {
		t.Fatalf("sink deliveries = %+v", delivered)
	}
}

func TestRestoreRebuildsRegistry(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	service, store := newTestService(now)
	point := registerPointWithCalibration(t, service, 0.1)
	if _, err := service.IngestSample(context.Background(), "chamber-1", point.ID, uuid.Nil, 0, 6.0, now); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := service.RecordManual(context.Background(), "chamber-1", point.ID, 6.08, now, "crew"); err != nil {
		t.Fatalf("manual: %v", err)
	}

	events, err := store.Events(context.Background(), "chamber-1", 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	restored, _ := newTestService(now)
	for _, event := range events {
		if err := restored.Restore(event); err != nil {
			t.Fatalf("restore %s: %v", event.Type, err)
		}
	}
	latest, ok := restored.LatestTrusted("chamber-1")
	if !ok {
		t.Fatal("restored registry lost the trusted reading")
	}
	if latest.Version != 1 || latest.CorrectedMeters != 6.1 {
		t.Fatalf("restored reading = %+v", latest)
	}
	if len(restored.Calibrations(point.ID)) != 1 {
		t.Fatal("restored registry lost calibration history")
	}
}
