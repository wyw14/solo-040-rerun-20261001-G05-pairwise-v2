package verification_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/verification"
)

type fakeSink struct {
	accepted []model.VerifiedReading
	fail     error
}

func (f *fakeSink) AcceptTrusted(reading model.VerifiedReading) error {
	if f.fail != nil {
		return f.fail
	}
	f.accepted = append(f.accepted, reading)
	return nil
}

func newService(t *testing.T, sink verification.TrustedReadingSink) (*verification.Service, journal.Store, time.Time) {
	t.Helper()
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return base }
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, clock)
	service := verification.NewService(recorder, verification.Config{
		DeviationToleranceM: 0.02,
		CompareWindow:       10 * time.Minute,
	}, clock)
	if sink != nil {
		if err := service.BindSink("chamber-1", sink); err != nil {
			t.Fatalf("bind sink: %v", err)
		}
	}
	return service, store, base
}

func seedBasics(t *testing.T, ctx context.Context, service *verification.Service, at time.Time, version string, offset, scale float64) {
	t.Helper()
	point, err := model.NewPointRegistration("chamber-1", "staff-1", "上游水尺", "probe-1", "", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterPoint(ctx, point); err != nil {
		t.Fatalf("register point: %v", err)
	}
	if _, err := service.RegisterCalibration(ctx, "chamber-1", "probe-1", version, offset, scale, at.Add(-time.Hour), "检修后校准"); err != nil {
		t.Fatalf("register calibration: %v", err)
	}
}

func addStaff(t *testing.T, ctx context.Context, service *verification.Service, at time.Time, meters float64) {
	t.Helper()
	staff, err := model.NewStaffReading("chamber-1", "staff-1", meters, at, "张三", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordStaff(ctx, staff); err != nil {
		t.Fatalf("record staff: %v", err)
	}
}

func TestVerifyTrustedWithinTolerance(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{}
	service, _, at := newService(t, sink)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)
	addStaff(t, ctx, service, at, 5.00)

	outcome, err := service.Verify(ctx, verification.ReportedReading{
		ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		CycleID: uuid.New(), Generation: 1, RawMeters: 5.01, ObservedAt: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if outcome.Reading.Trust != model.TrustTrusted {
		t.Fatalf("trust = %s (%s), want trusted", outcome.Reading.Trust, outcome.Reading.ReviewReason)
	}
	if outcome.Reading.CalibrationVer != "cal-v1" {
		t.Fatalf("calibration version = %q, want cal-v1", outcome.Reading.CalibrationVer)
	}
	if !outcome.WindowAccepted || len(sink.accepted) != 1 {
		t.Fatalf("trusted reading must reach the window sink, accepted=%v", outcome.WindowAccepted)
	}
}

func TestVerifyPendingWhenDeviationExceedsTolerance(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{}
	service, _, at := newService(t, sink)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)
	addStaff(t, ctx, service, at, 5.00)

	outcome, err := service.Verify(ctx, verification.ReportedReading{
		ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		RawMeters: 5.20, ObservedAt: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if outcome.Reading.Trust != model.TrustPending {
		t.Fatalf("trust = %s, want pending_review", outcome.Reading.Trust)
	}
	if len(sink.accepted) != 0 {
		t.Fatalf("pending reading must not reach the window sink")
	}
}

func TestVerifyPendingWithoutStaffReading(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{}
	service, _, at := newService(t, sink)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)

	outcome, err := service.Verify(ctx, verification.ReportedReading{
		ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		RawMeters: 5.00, ObservedAt: at,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if outcome.Reading.Trust != model.TrustPending {
		t.Fatalf("trust = %s, want pending_review without staff cross-check", outcome.Reading.Trust)
	}
}

// 检修登记新校准版本不得覆盖旧闸次依据：旧读数仍记录其当时的版本与校准值，
// 新版本只影响其生效时间之后的读数。
func TestNewCalibrationDoesNotRewriteHistory(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{}
	service, _, at := newService(t, sink)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)
	addStaff(t, ctx, service, at, 5.00)

	oldCycle := uuid.New()
	outcome, err := service.Verify(ctx, verification.ReportedReading{
		ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		CycleID: oldCycle, Generation: 1, RawMeters: 5.005, ObservedAt: at,
	})
	if err != nil {
		t.Fatalf("verify old: %v", err)
	}
	if outcome.Reading.Trust != model.TrustTrusted || outcome.Reading.CalibrationVer != "cal-v1" {
		t.Fatalf("old reading basis changed: %+v", outcome.Reading)
	}

	// Maintenance: probe drifts, technician registers a new offset version.
	if _, err := service.RegisterCalibration(ctx, "chamber-1", "probe-1", "cal-v2", 0.10, 1, at.Add(time.Hour), "探头更换"); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	history := service.Calibrations("chamber-1", "probe-1")
	if len(history) != 2 || history[0].Version != "cal-v1" || history[1].Version != "cal-v2" {
		t.Fatalf("calibration history must be append-only, got %+v", history)
	}

	// Historical reading keeps its original version and calibrated value.
	stored, ok := service.Reading(outcome.Reading.ID)
	if !ok || stored.CalibrationVer != "cal-v1" || stored.Meters != outcome.Reading.Meters {
		t.Fatalf("historical reading was rewritten: %+v", stored)
	}

	// A reading before v2's effective time still resolves v1.
	current, ok := service.CurrentCalibration("chamber-1", "probe-1", at.Add(30*time.Minute))
	if !ok || current.Version != "cal-v1" {
		t.Fatalf("calibration in force before v2 effective = %v, want cal-v1", current.Version)
	}
	after, ok := service.CurrentCalibration("chamber-1", "probe-1", at.Add(2*time.Hour))
	if !ok || after.Version != "cal-v2" {
		t.Fatalf("calibration in force after maintenance = %v, want cal-v2", current.Version)
	}
}

func TestReviewClearsPendingAndFeedsSink(t *testing.T) {
	ctx := context.Background()
	sink := &fakeSink{}
	service, _, at := newService(t, sink)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)
	addStaff(t, ctx, service, at, 5.00)

	outcome, err := service.Verify(ctx, verification.ReportedReading{
		ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		RawMeters: 5.10, ObservedAt: at,
	})
	if err != nil || outcome.Reading.Trust != model.TrustPending {
		t.Fatalf("setup: %v %v", err, outcome.Reading.Trust)
	}
	cleared, err := service.Review(ctx, outcome.Reading.ID, true, "现场复尺一致，操作员张三")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if cleared.Trust != model.TrustTrusted || len(sink.accepted) != 1 {
		t.Fatalf("reviewed reading must be trusted and forwarded, trust=%s sink=%d", cleared.Trust, len(sink.accepted))
	}
	// A trusted reading is a historical basis and cannot be downgraded.
	if _, err := service.Review(ctx, outcome.Reading.ID, false, "attempted rollback"); err == nil {
		t.Fatalf("downgrading a trusted reading must be rejected")
	}
}

func TestDuplicatePointAndCalibrationRejected(t *testing.T) {
	ctx := context.Background()
	service, _, at := newService(t, nil)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)
	point, _ := model.NewPointRegistration("chamber-1", "staff-1", "", "probe-1", "", at)
	if _, err := service.RegisterPoint(ctx, point); err == nil {
		t.Fatal("duplicate point registration must be rejected")
	}
	if _, err := service.RegisterCalibration(ctx, "chamber-1", "probe-1", "cal-v1", 0, 1, at, ""); err == nil {
		t.Fatal("duplicate calibration version must be rejected")
	}
}

func TestRestoreRebuildsRegister(t *testing.T) {
	ctx := context.Background()
	service, store, at := newService(t, nil)
	seedBasics(t, ctx, service, at, "cal-v1", 0, 1)
	addStaff(t, ctx, service, at, 5.00)
	outcome, err := service.Verify(ctx, verification.ReportedReading{
		ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		RawMeters: 5.01, ObservedAt: at,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	rebuilt := verification.NewService(journal.NewRecorder(store, func() time.Time { return at }), verification.Config{}, func() time.Time { return at })
	restore := verification.NewRestore(store, rebuilt)
	if err := restore.RestoreAll(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(rebuilt.Points("chamber-1")) != 1 {
		t.Fatalf("points not restored")
	}
	if cals := rebuilt.Calibrations("chamber-1", "probe-1"); len(cals) != 1 || cals[0].Version != "cal-v1" {
		t.Fatalf("calibrations not restored: %+v", cals)
	}
	reading, ok := rebuilt.Reading(outcome.Reading.ID)
	if !ok || reading.Trust != model.TrustTrusted || reading.CalibrationVer != "cal-v1" {
		t.Fatalf("reading not restored with its basis: %+v ok=%v", reading, ok)
	}
}
