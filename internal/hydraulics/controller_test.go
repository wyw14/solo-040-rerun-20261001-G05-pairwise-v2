package hydraulics

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

func trustedReading(cycleID uuid.UUID, generation uint64, version string, meters float64, at time.Time) model.VerifiedReading {
	return model.VerifiedReading{
		ID: uuid.New(), ChamberID: "chamber-1", PointID: "staff-1", DeviceID: "probe-1",
		CalibrationID: uuid.New(), CalibrationVer: version, Meters: meters,
		Trust: model.TrustTrusted, CycleID: cycleID, Generation: generation, ObservedAt: at,
	}
}

func TestAcceptTrustedFeedsWindowAndInterlockView(t *testing.T) {
	controller := NewController(nil, nil, NewRamp(0, time.Now), time.Now)
	cycleID := uuid.New()
	cycle := model.TransitCycle{ID: cycleID, ChamberID: "chamber-1", Generation: 1, Stage: model.StageLeveling}
	if err := controller.BeginLeveling(cycle); err != nil {
		t.Fatalf("begin: %v", err)
	}
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		reading := trustedReading(cycleID, 1, "cal-v1", 5.00, at.Add(time.Duration(i)*time.Second))
		if err := controller.AcceptTrusted(reading); err != nil {
			t.Fatalf("accept trusted %d: %v", i, err)
		}
	}
	sample, ok := controller.TrustedSample("chamber-1")
	if !ok {
		t.Fatal("trusted sample missing")
	}
	if sample.CalibVer != "cal-v1" || sample.Meters != 5.00 {
		t.Fatalf("sample basis lost: %+v", sample)
	}
}

func TestAcceptTrustedRejectsForeignGeneration(t *testing.T) {
	controller := NewController(nil, nil, NewRamp(0, time.Now), time.Now)
	cycleID := uuid.New()
	if err := controller.BeginLeveling(model.TransitCycle{ID: cycleID, ChamberID: "chamber-1", Generation: 2, Stage: model.StageLeveling}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	err := controller.AcceptTrusted(trustedReading(cycleID, 1, "cal-v1", 5.0, time.Now()))
	if !errors.Is(err, model.ErrReadingNotCurrentGeneration) {
		t.Fatalf("foreign generation must be refused with sentinel, got %v", err)
	}
}

func TestWindowRejectsSampleWithoutCalibrationVersion(t *testing.T) {
	window, err := NewLevelWindow(4)
	if err != nil {
		t.Fatal(err)
	}
	cycleID := uuid.New()
	window.Begin(cycleID, 1)
	err = window.Add(model.LevelSample{CycleID: cycleID, Generation: 1, Meters: 5.0, ObservedAt: time.Now()})
	if err == nil {
		t.Fatal("window must reject verified sample missing its calibration version")
	}
}
