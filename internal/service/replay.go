package service

import (
    "encoding/json"
    "fmt"
    "time"

    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
)

// ReplayScenario rebuilds a run timeline from its durable journal records.
// Events backed by a record are marked "persisted"; process steps that the
// stage sequence or measurement window expects but never committed are marked
// "missing" so a reader can tell real observations apart from gaps.
func ReplayScenario(name string, run model.RunID, revision model.RevisionID, records []journal.Record) *Scenario {
    runRecords := journal.RecordsForRun(records, run)
    observed := make([]ScenarioEvent, 0, len(runRecords))

    stageAt := make(map[model.RunStage]time.Time)
    hasWave, hasForce := false, false
    var started, finished time.Time
    var lastAt time.Time
    var firstEvidence time.Time

    for _, record := range runRecords {
        event, stage, evidenceKind := recordEvent(run, record)
        observed = append(observed, event)
        lastAt = record.At
        if started.IsZero() || record.At.Before(started) {
            started = record.At
        }
        if stage != "" {
            stageAt[stage] = record.At
            if stage == model.StageComplete || stage == model.StageAborted {
                finished = record.At
            }
        }
        switch evidenceKind {
        case "wave":
            hasWave = true
            if firstEvidence.IsZero() || record.At.Before(firstEvidence) {
                firstEvidence = record.At
            }
        case "force":
            hasForce = true
            if firstEvidence.IsZero() || record.At.Before(firstEvidence) {
                firstEvidence = record.At
            }
        }
    }

    if len(runRecords) == 0 {
        return NewScenario(name, run, revision, time.Time{})
    }

    missing := missingSegments(run, stageAt, firstEvidence, hasWave, hasForce, lastAt)
    observed = append(observed, missing...)

    scenario := NewScenario(name, run, revision, started)
    scenario.Reconcile(observed, finished)
    return scenario
}

// recordEvent maps one durable record to a timeline event. The second return
// value is the stage it recorded (empty for non-stage records) and the third
// the evidence family ("wave"/"force").
func recordEvent(run model.RunID, record journal.Record) (ScenarioEvent, model.RunStage, string) {
    event := ScenarioEvent{ID: record.ID, Run: run, At: record.At.UTC(), Source: EventSourcePersisted}
    switch record.Kind {
    case "stage":
        var stage model.RunStage
        if err := json.Unmarshal(record.Payload, &stage); err != nil {
            stage = model.RunStage("unknown")
        }
        event.Kind = string(stage)
        event.Component = "test"
        event.Detail = "stage transition persisted"
        if stage == model.StageAborted {
            event.Kind = "fault"
            event.Detail = "run aborted"
        }
        return event, stage, ""
    case "fence":
        var fence uint64
        if json.Unmarshal(record.Payload, &fence) == nil {
            event.Detail = fmt.Sprintf("exclusive track fence %d acquired", fence)
        } else {
            event.Detail = "track fence acquired"
        }
        event.Kind = "lease"
        event.Component = "track"
    case "command":
        var command model.DeviceCommand
        event.Kind = "command"
        event.Component = "carriage"
        if json.Unmarshal(record.Payload, &command) == nil {
            event.Detail = fmt.Sprintf("epoch %d setpoint %.3f committed", command.Epoch, command.Setpoint)
        } else {
            event.Detail = "carriage command committed"
        }
    case "wave_trace":
        event.Kind = "sample"
        event.Component = "probe"
        event.Detail = "wave frame recovered from journal"
        return event, "", "wave"
    case "force_trace":
        event.Kind = "sample"
        event.Component = "force"
        event.Detail = "force frame recovered from journal"
        return event, "", "force"
    default:
        event.Kind = record.Kind
        event.Component = "journal"
        event.Detail = "record recovered from journal"
    }
    return event, "", ""
}

// missingSegments derives the gaps: stages the run passed through without a
// stage record, and wave/force frames never committed once the measurement
// window had begun (a measuring stage record or any captured evidence).
func missingSegments(run model.RunID, stageAt map[model.RunStage]time.Time, firstEvidence time.Time, hasWave, hasForce bool, lastAt time.Time) []ScenarioEvent {
    missing := make([]ScenarioEvent, 0)
    sequence := model.StageSequence()

    highest := -1
    for index, stage := range sequence {
        if _, ok := stageAt[stage]; ok {
            if index > highest {
                highest = index
            }
        }
    }

    // Missing stages are inserted just before the next stage that was recorded.
    for index := 1; index <= highest; index++ {
        stage := sequence[index]
        if _, ok := stageAt[stage]; ok {
            continue
        }
        nextAt := lastAt
        gap := 1
        for later := index + 1; later <= highest; later++ {
            if at, found := stageAt[sequence[later]]; found {
                nextAt = at
                gap = later - index
                break
            }
        }
        event := ScenarioEvent{
            ID: model.NewEventID(),
            Run: run,
            Kind: string(stage),
            Component: "test",
            Detail: "no persisted record for stage " + string(stage),
            At: nextAt.Add(-time.Duration(gap) * time.Nanosecond),
            Source: EventSourceMissing,
        }
        missing = append(missing, event)
    }

    measuring, measuringRecorded := stageAt[model.StageMeasuring]
    windowOpen := measuringRecorded || !firstEvidence.IsZero()
    if windowOpen {
        boundary := lastAt.Add(time.Nanosecond)
        for _, stage := range []model.RunStage{model.StageDecelerating, model.StageSettling, model.StageComplete} {
            if at, ok := stageAt[stage]; ok {
                boundary = at
                break
            }
        }
        if measuringRecorded && measuring.Before(boundary) {
            boundary = measuring.Add(boundary.Sub(measuring) / 2)
        }
        if !hasWave {
            missing = append(missing, ScenarioEvent{
                ID: model.NewEventID(), Run: run, Kind: "sample", Component: "probe",
                Detail: "no persisted wave frame for measurement window",
                At: boundary.Add(-2 * time.Nanosecond), Source: EventSourceMissing,
            })
        }
        if !hasForce {
            missing = append(missing, ScenarioEvent{
                ID: model.NewEventID(), Run: run, Kind: "sample", Component: "force",
                Detail: "no persisted force frame for measurement window",
                At: boundary.Add(-time.Nanosecond), Source: EventSourceMissing,
            })
        }
    }
    return missing
}
