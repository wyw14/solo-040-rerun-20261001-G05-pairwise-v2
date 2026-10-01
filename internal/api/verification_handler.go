package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/verification"
)

type pointInput struct {
	PointID   string `json:"point_id"`
	Label     string `json:"label"`
	DeviceID  string `json:"device_id"`
	ZeroDatum string `json:"zero_datum"`
}

func registerPointHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input pointInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		chamberID := chi.URLParam(r, "chamberID")
		registration, err := model.NewPointRegistration(chamberID, input.PointID, input.Label, input.DeviceID, input.ZeroDatum, runtime.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := runtime.Verification.RegisterPoint(r.Context(), registration)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, saved)
	}
}

type calibrationInput struct {
	DeviceID      string     `json:"device_id"`
	Version       string     `json:"version"`
	OffsetM       float64    `json:"offset_m"`
	Scale         float64    `json:"scale"`
	EffectiveFrom *time.Time `json:"effective_from"`
	Note          string     `json:"note"`
}

func registerCalibrationHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input calibrationInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		effectiveFrom := time.Time{}
		if input.EffectiveFrom != nil {
			effectiveFrom = *input.EffectiveFrom
		}
		record, err := runtime.Verification.RegisterCalibration(
			r.Context(), chi.URLParam(r, "chamberID"), input.DeviceID,
			input.Version, input.OffsetM, input.Scale, effectiveFrom, input.Note,
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, record)
	}
}

type staffInput struct {
	PointID    string    `json:"point_id"`
	Meters     float64   `json:"meters"`
	ObservedAt time.Time `json:"observed_at"`
	Reader     string    `json:"reader"`
	Note       string    `json:"note"`
}

func staffReadingHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input staffInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if input.ObservedAt.IsZero() {
			input.ObservedAt = runtime.Now()
		}
		reading, err := model.NewStaffReading(
			chi.URLParam(r, "chamberID"), input.PointID, input.Meters,
			input.ObservedAt, input.Reader, input.Note,
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := runtime.Verification.RecordStaff(r.Context(), reading)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, saved)
	}
}

type readingInput struct {
	PointID    string     `json:"point_id"`
	DeviceID   string     `json:"device_id"`
	RawMeters  float64    `json:"raw_meters"`
	ObservedAt *time.Time `json:"observed_at"`
	CycleID    string     `json:"cycle_id"`
	Generation uint64     `json:"generation"`
}

func reportedReadingHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input readingInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if input.RawMeters < 0 {
			writeError(w, http.StatusBadRequest, errors.New("raw_meters cannot be negative"))
			return
		}
		chamberID := chi.URLParam(r, "chamberID")
		reported := verification.ReportedReading{
			ChamberID: chamberID, PointID: input.PointID, DeviceID: input.DeviceID,
			RawMeters: input.RawMeters,
		}
		if input.ObservedAt != nil {
			reported.ObservedAt = *input.ObservedAt
		}
		// Attach the reading to a transit generation. Explicit values win;
		// otherwise fall back to the chamber's current transit so reports
		// during leveling feed its window once trusted.
		if input.CycleID != "" || input.Generation != 0 {
			cycleID, err := parseUUID(input.CycleID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			reported.CycleID, reported.Generation = cycleID, input.Generation
		} else if active, ok := runtime.Cycles.Current(chamberID); ok && !active.Stage.Terminal() {
			reported.CycleID, reported.Generation = active.ID, active.Generation
		}
		outcome, err := runtime.Verification.Verify(r.Context(), reported)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, outcome)
	}
}

func listReadingsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trust := model.ReadingTrust(r.URL.Query().Get("trust"))
		if trust != "" && !trust.Valid() {
			writeError(w, http.StatusBadRequest, errors.New("trust must be trusted or pending_review"))
			return
		}
		writeJSON(w, http.StatusOK, runtime.Verification.Readings(chi.URLParam(r, "chamberID"), trust))
	}
}

func listCalibrationsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, runtime.Verification.Calibrations(chi.URLParam(r, "chamberID"), r.URL.Query().Get("device_id")))
	}
}

func listStaffHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, runtime.Verification.StaffReadings(chi.URLParam(r, "chamberID"), r.URL.Query().Get("point_id")))
	}
}

func listPointsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, runtime.Verification.Points(r.URL.Query().Get("chamber_id")))
	}
}

type reviewInput struct {
	Trusted bool   `json:"trusted"`
	Reason  string `json:"reason"`
}

func reviewReadingHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input reviewInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if input.Reason == "" {
			writeError(w, http.StatusBadRequest, errors.New("review reason is required"))
			return
		}
		readingID, err := uuid.Parse(chi.URLParam(r, "readingID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		reading, err := runtime.Verification.Review(r.Context(), readingID, input.Trusted, input.Reason)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, reading)
	}
}
