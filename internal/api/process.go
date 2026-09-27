package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

type advanceStageRequest struct {
	Stage model.RunStage `json:"stage"`
	At    *time.Time     `json:"at"`
}

type leaseRequest struct {
	Fence uint64     `json:"fence"`
	At    *time.Time `json:"at"`
}

func optionalAt(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func writeProcessError(w http.ResponseWriter, err error) {
	if err.Error() == "run not found" {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func (r *Router) measuredRunTimeline(w http.ResponseWriter, request *http.Request) {
	timeline, err := r.dependencies.MeasuredRunTimeline(model.RunID(chi.URLParam(request, "id")))
	if err != nil {
		writeProcessError(w, err)
		return
	}
	writeJSON(w, timeline)
}

func (r *Router) advanceMeasuredStage(w http.ResponseWriter, request *http.Request) {
	var input advanceStageRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run, err := r.dependencies.AdvanceMeasuredRun(
		model.RunID(chi.URLParam(request, "id")),
		input.Stage,
		optionalAt(input.At),
	)
	if err != nil {
		writeProcessError(w, err)
		return
	}
	writeJSON(w, run)
}

func (r *Router) leaseMeasuredRun(w http.ResponseWriter, request *http.Request) {
	var input leaseRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fence, err := r.dependencies.LeaseMeasuredRun(
		model.RunID(chi.URLParam(request, "id")),
		input.Fence,
		optionalAt(input.At),
	)
	if err != nil {
		writeProcessError(w, err)
		return
	}
	writeJSON(w, map[string]any{"fence": fence})
}

func (r *Router) recordMeasuredCommand(w http.ResponseWriter, request *http.Request) {
	var input service.CommandInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	command, err := r.dependencies.RecordMeasuredCommand(model.RunID(chi.URLParam(request, "id")), input)
	if err != nil {
		writeProcessError(w, err)
		return
	}
	writeJSON(w, command)
}
