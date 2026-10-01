package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type pointInput struct {
	PointID string `json:"point_id"`
	Label   string `json:"label"`
}

func registerPointHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input pointInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		point, err := runtime.LevelCheck.RegisterPoint(r.Context(), chi.URLParam(r, "chamberID"), input.PointID, input.Label)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, point)
	}
}

type pointView struct {
	model.MeasurementPoint
	CurrentCalibration *model.CalibrationVersion `json:"current_calibration,omitempty"`
}

func listPointsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		points := runtime.LevelCheck.Points(chamberID)
		views := make([]pointView, 0, len(points))
		for _, point := range points {
			view := pointView{MeasurementPoint: point}
			if calibration, ok := runtime.LevelCheck.CurrentCalibration(point.ID); ok {
				current := calibration
				view.CurrentCalibration = &current
			}
			views = append(views, view)
		}
		writeJSON(w, http.StatusOK, views)
	}
}

type calibrationInput struct {
	OffsetM  float64 `json:"offset_m"`
	IssuedBy string  `json:"issued_by"`
	Note     string  `json:"note"`
}

func issueCalibrationHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input calibrationInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		calibration, err := runtime.LevelCheck.IssueCalibration(
			r.Context(), chi.URLParam(r, "chamberID"), chi.URLParam(r, "pointID"), input.OffsetM, input.IssuedBy, input.Note)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, calibration)
	}
}

func listCalibrationsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, runtime.LevelCheck.Calibrations(chi.URLParam(r, "pointID")))
	}
}

type manualInput struct {
	PointID    string     `json:"point_id"`
	Meters     float64    `json:"meters"`
	ObservedAt *time.Time `json:"observed_at"`
	Reader     string     `json:"reader"`
}

func recordManualHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input manualInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		observedAt := time.Time{}
		if input.ObservedAt != nil {
			observedAt = *input.ObservedAt
		}
		manual, err := runtime.LevelCheck.RecordManual(r.Context(), chi.URLParam(r, "chamberID"), input.PointID, input.Meters, observedAt, input.Reader)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, manual)
	}
}

type sampleInput struct {
	PointID    string     `json:"point_id"`
	CycleID    string     `json:"cycle_id"`
	Generation uint64     `json:"generation"`
	Meters     float64    `json:"meters"`
	ObservedAt *time.Time `json:"observed_at"`
}

func ingestSampleHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input sampleInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cycleID := uuid.Nil
		if input.CycleID != "" {
			parsed, err := parseUUID(input.CycleID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			cycleID = parsed
		}
		if cycleID != uuid.Nil && input.Generation == 0 {
			writeError(w, http.StatusBadRequest, errors.New("generation is required with cycle_id"))
			return
		}
		observedAt := time.Time{}
		if input.ObservedAt != nil {
			observedAt = *input.ObservedAt
		}
		reading, err := runtime.LevelCheck.IngestSample(r.Context(), chi.URLParam(r, "chamberID"), input.PointID, cycleID, input.Generation, input.Meters, observedAt)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, reading)
	}
}

func listReadingsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := model.ReadingStatus(r.URL.Query().Get("status"))
		if status != "" && !status.Valid() {
			writeError(w, http.StatusBadRequest, errors.New("invalid reading status filter"))
			return
		}
		writeJSON(w, http.StatusOK, runtime.LevelCheck.Readings(chi.URLParam(r, "chamberID"), status))
	}
}

type reviewInput struct {
	Decision string `json:"decision"`
	Reviewer string `json:"reviewer"`
	Note     string `json:"note"`
}

func reviewReadingHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input reviewInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		readingID, err := parseUUID(chi.URLParam(r, "readingID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		reading, err := runtime.LevelCheck.Review(r.Context(), readingID, model.ReadingStatus(input.Decision), input.Reviewer, input.Note)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, reading)
	}
}
