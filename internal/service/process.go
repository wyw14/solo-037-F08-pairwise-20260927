package service

import (
	"errors"
	"math"
	"time"

	"github.com/wyw14/solo-037/internal/journal"
	"github.com/wyw14/solo-037/internal/model"
)

// CommandInput is the operator-supplied carriage command for a measured run.
// A zero IssuedAt is stamped with the server clock.
type CommandInput struct {
	Epoch    uint64    `json:"epoch"`
	Fence    uint64    `json:"fence"`
	Setpoint float64   `json:"setpoint"`
	IssuedAt time.Time `json:"issued_at"`
}

func normalizeAt(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

// CreateMeasuredRun creates the operator run and journals its configured
// stage, so the process timeline starts immediately.
func (r *Runtime) CreateMeasuredRun(name, modelName, section, carriage string) (MeasuredRun, error) {
	run, err := r.Measurements.Create(name, modelName, section, carriage)
	if err != nil {
		return MeasuredRun{}, err
	}
	record, err := journal.StageRecord(run.Operation.ID, model.StageConfigured, run.Operation.CreatedAt)
	if err != nil {
		return MeasuredRun{}, err
	}
	if err := r.process.Append(record); err != nil {
		return MeasuredRun{}, err
	}
	return run, nil
}

// RecordMeasurement stores the frame in the operator archive (analysis source)
// and appends a measurement record to the process journal (timeline source).
func (r *Runtime) RecordMeasurement(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	evidence, err := r.Measurements.Record(id, input)
	if err != nil {
		return model.Evidence{}, err
	}
	record, err := journal.EvidenceRecord(evidence, evidence.RecordedAt)
	if err != nil {
		return model.Evidence{}, err
	}
	if err := r.process.Append(record); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

// AdvanceMeasuredRun validates the stage transition against the archive and
// appends the persisted stage record.
func (r *Runtime) AdvanceMeasuredRun(id model.RunID, stage model.RunStage, at time.Time) (MeasuredRun, error) {
	at = normalizeAt(at)
	run, err := r.Measurements.Advance(id, stage, at)
	if err != nil {
		return MeasuredRun{}, err
	}
	record, err := journal.StageRecord(id, stage, at)
	if err != nil {
		return MeasuredRun{}, err
	}
	if err := r.process.Append(record); err != nil {
		return MeasuredRun{}, err
	}
	return run, nil
}

// LeaseMeasuredRun records the exclusive track fence granted to the run.
func (r *Runtime) LeaseMeasuredRun(id model.RunID, fence uint64, at time.Time) (uint64, error) {
	if fence == 0 {
		return 0, errors.New("fence must be greater than zero")
	}
	if _, ok := r.Measurements.Get(id); !ok {
		return 0, errors.New("run not found")
	}
	at = normalizeAt(at)
	record, err := journal.FenceRecord(id, fence, at)
	if err != nil {
		return 0, err
	}
	if err := r.process.Append(record); err != nil {
		return 0, err
	}
	return fence, nil
}

// RecordMeasuredCommand persists a carriage acceleration command, so commands
// survive restart and appear in the reconstructed timeline.
func (r *Runtime) RecordMeasuredCommand(id model.RunID, input CommandInput) (model.DeviceCommand, error) {
	if _, ok := r.Measurements.Get(id); !ok {
		return model.DeviceCommand{}, errors.New("run not found")
	}
	if input.Epoch == 0 {
		return model.DeviceCommand{}, errors.New("epoch must be greater than zero")
	}
	if math.IsNaN(input.Setpoint) || math.IsInf(input.Setpoint, 0) {
		return model.DeviceCommand{}, errors.New("setpoint must be finite")
	}
	at := normalizeAt(input.IssuedAt)
	if at.After(time.Now().Add(time.Minute)) {
		return model.DeviceCommand{}, errors.New("command issued_at is in the future")
	}
	command := model.DeviceCommand{
		ID:       string(model.NewEventID()),
		Run:      id,
		Epoch:    input.Epoch,
		Fence:    input.Fence,
		Setpoint: input.Setpoint,
		IssuedAt: at,
	}
	record, err := journal.CommandRecord(id, command, at)
	if err != nil {
		return model.DeviceCommand{}, err
	}
	if err := r.process.Append(record); err != nil {
		return model.DeviceCommand{}, err
	}
	return command, nil
}

// MeasuredRunTimeline rebuilds the real run timeline from its persisted
// stage, command and measurement records.
func (r *Runtime) MeasuredRunTimeline(id model.RunID) (ProcessTimeline, error) {
	run, ok := r.Measurements.Get(id)
	if !ok {
		return ProcessTimeline{}, errors.New("run not found")
	}
	records := journal.RecordsForRun(r.process.Records(), id)
	meta := TimelineMeta{
		Name:     run.Operation.Name,
		Run:      id,
		Revision: run.Operation.Revision,
		Started:  run.Operation.CreatedAt,
	}
	return ReconstructTimeline(meta, records), nil
}
