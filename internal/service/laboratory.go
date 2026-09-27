package service

import (
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/absorber"
    "github.com/wyw14/solo-037/internal/carriage"
    "github.com/wyw14/solo-037/internal/emergency"
    "github.com/wyw14/solo-037/internal/force"
    "github.com/wyw14/solo-037/internal/journal"
    "github.com/wyw14/solo-037/internal/model"
    "github.com/wyw14/solo-037/internal/permit"
    "github.com/wyw14/solo-037/internal/probe"
    "github.com/wyw14/solo-037/internal/safety"
    "github.com/wyw14/solo-037/internal/tank"
    "github.com/wyw14/solo-037/internal/telemetry"
    testpkg "github.com/wyw14/solo-037/internal/test"
    "github.com/wyw14/solo-037/internal/track"
    "github.com/wyw14/solo-037/internal/wave"
)

type Laboratory struct {
    Store *journal.Store
    Snapshots *journal.SnapshotStore
    Tank *tank.Registry
    Sections *tank.Sections
    Boundary *tank.Boundary
    Tracks *track.Coordinator
    TrackState *track.State
    Generation *track.Generation
    Carriage *carriage.Controller
    Position *carriage.PositionState
    Wave *wave.Controller
    Probes *probe.Collection
    Forces *force.Receiver
    Permits *permit.Evaluator
    PermitCoordinator *permit.Coordinator
    Absorber *absorber.Controller
    Emergency *emergency.Brake
    Interlocks *safety.Interlocks
    Metrics *telemetry.Metrics
    Tracer *telemetry.Tracer
    Tests *testpkg.Registry
}

func NewLaboratory(directory string) (*Laboratory, error) {
    store, err := journal.NewStore(filepath.Join(directory, "journal"))
    if err != nil {
        return nil, err
    }
    snapshots, err := journal.NewSnapshotStore(filepath.Join(directory, "snapshots"))
    if err != nil {
        return nil, err
    }
    tankRegistry := tank.NewRegistry(2.4)
    sections := tank.NewSections([]string{"section-a", "section-b"}, 2.4)
    boundary := tank.NewBoundary()
    tracks := track.NewCoordinator(tankRegistry)
    trackState := &track.State{}
    generation := track.NewGeneration()
    carriageController := carriage.NewController()
    waveController := wave.NewController()
    evaluator := permit.NewEvaluator()
    absorberController := absorber.NewController(boundary)
    return &Laboratory{
        Store: store,
        Snapshots: snapshots,
        Tank: tankRegistry,
        Sections: sections,
        Boundary: boundary,
        Tracks: tracks,
        TrackState: trackState,
        Generation: generation,
        Carriage: carriageController,
        Position: carriage.NewPositionState(generation.Revision()),
        Wave: waveController,
        Probes: probe.NewCollection(),
        Forces: force.NewReceiver(),
        Permits: evaluator,
        PermitCoordinator: permit.NewCoordinator(evaluator),
        Absorber: absorberController,
        Emergency: emergency.NewBrake(carriageController, waveController),
        Interlocks: safety.NewInterlocks(),
        Metrics: &telemetry.Metrics{},
        Tracer: telemetry.NewTracer(),
        Tests: testpkg.NewRegistry(),
    }, nil
}

func (l *Laboratory) Exercise(at time.Time) map[string]any {
    span := l.Tracer.Start("laboratory.exercise", at)
    operation := model.NewOperation("commissioning", "benchmark-hull", "section-a", "carriage-1", at)
    l.Tests.Put(operation)
    startup := testpkg.Startup{Tests: l.Tests, Tracks: l.Tracks, Carriage: l.Carriage}
    lease, leaseErr := startup.Arm(operation, operation.Section, at)
    l.Metrics.Request()
    if leaseErr != nil {
        l.Metrics.Fault()
        l.Tracer.End(span, at)
        return map[string]any{"error": leaseErr.Error()}
    }
    l.TrackState.Assign(operation.ID, lease.Fence)
    stageRecord, stageErr := journal.StageRecord(operation.ID, model.StageConfigured, at)
    if stageErr == nil {
        _ = l.Store.Append(stageRecord)
    }
    if fenceRecord, fenceErr := journal.FenceRecord(operation.ID, lease.Fence, at); fenceErr == nil {
        _ = l.Store.Append(fenceRecord)
    }
    if armingRecord, armingErr := journal.StageRecord(operation.ID, model.StageArming, at); armingErr == nil {
        _ = l.Store.Append(armingRecord)
    }
    command := l.Carriage.Accelerate(operation.ID, 1, lease.Fence, 1.4, at.Add(time.Millisecond))
    if commandRecord, commandErr := journal.CommandRecord(command, command.IssuedAt); commandErr == nil {
        _ = l.Store.Append(commandRecord)
    }
    if acceleratingRecord, acceleratingErr := journal.StageRecord(operation.ID, model.StageAccelerating, command.IssuedAt); acceleratingErr == nil {
        _ = l.Store.Append(acceleratingRecord)
    }
    l.Metrics.Event()
    l.Position.SetWindow(5, 15)
    positionWindow := l.Position.Observe(10)
    profile := wave.NewProfile(0.8, 0.01, 30*time.Millisecond)
    if measuringRecord, measuringErr := journal.StageRecord(operation.ID, model.StageMeasuring, at.Add(2*time.Millisecond)); measuringErr == nil {
        _ = l.Store.Append(measuringRecord)
    }
    profileEvidence := wave.ProfileEvidence(operation.ID, operation.Revision, profile, at.Add(2*time.Millisecond))
    for index, evidence := range profileEvidence {
        evidence.SourceID = "probe-" + string(rune('a'+index%2))
        evidence.Durable = true
        l.Probes.Add(evidence)
        l.Sections.RecordWave(operation.Section, evidence.Value, evidence.RecordedAt)
        record, recordErr := journal.EvidenceRecord(evidence, evidence.RecordedAt)
        if recordErr == nil {
            _ = l.Store.Append(record)
        }
    }
    forceEvidence := l.Forces.Record(operation.ID, operation.Revision, 12.5, at.Add(3*time.Millisecond))
    forceRecord, forceErr := journal.EvidenceRecord(forceEvidence, forceEvidence.RecordedAt)
    if forceErr == nil {
        _ = l.Store.Append(forceRecord)
    }
    analysis := probe.Analyze(l.Probes.Samples(operation.ID))
    policy := permit.DefaultPolicy()
    policyDecision := policy.Evaluate(operation.ID, l.Probes.Samples(operation.ID), true, false, at.Add(4*time.Millisecond))
    l.Permits.SetSources(operation.ID, len(analysis.Sources))
    l.Permits.SetCalm(operation.ID, analysis.RMS <= 0.03)
    permitResult := l.PermitCoordinator.Update(operation.ID)
    _ = l.Absorber.SwitchMode("active", at.Add(5*time.Millisecond))
    reflectionReady := absorber.CalibrateFromProbe(l.Absorber, l.Probes.Samples(operation.ID), operation.Revision)
    receipt, brakeErr := l.Carriage.Brake(operation.ID, command.ID, command.Epoch, at.Add(6*time.Millisecond))
    if brakeErr == nil {
        l.Permits.ObserveBrake(operation.ID, receipt)
    }
    emergencyResult := l.Emergency.Execute(operation.ID, command.ID, command.Epoch, brakeErr, at.Add(7*time.Millisecond))
    for index, stage := range []model.RunStage{model.StageDecelerating, model.StageSettling, model.StageComplete} {
        record, recordErr := journal.StageRecord(operation.ID, stage, at.Add(time.Duration(7+index)*time.Millisecond))
        if recordErr == nil {
            _ = l.Store.Append(record)
        }
    }
    l.TrackState.Clear(operation.ID, lease.Fence)
    _ = l.Tracks.Release(operation.ID)
    records := l.Store.Records()
    snapshot, snapshotErr := l.Snapshots.SaveRecords(records, at.Add(8*time.Millisecond))
    scenario := StandardScenario(operation.ID, operation.Revision, at)
    merged := MergeScenarios("commissioning evidence", scenario)
    replay := ReplayScenario("commissioning replay", operation.ID, operation.Revision, records)
    audit := AuditLaboratory(l, operation.ID, lease.Fence, at.Add(9*time.Millisecond))
    reachability := ExerciseReachability(l, at.Add(10*time.Millisecond))
    l.Tracer.End(span, at.Add(9*time.Millisecond))
    return map[string]any{
        "operation": operation,
        "lease": track.LeaseDescription(lease),
        "position": positionWindow,
        "analysis": analysis,
        "policy": policyDecision,
        "permit": permitResult,
        "missing": permit.Missing(policyDecision),
        "requirements": permit.RequirementMap(policyDecision),
        "reflection_ready": reflectionReady,
        "emergency": emergencyResult,
        "journal": journal.Summarize(records),
        "snapshot_runs": journal.SnapshotRuns(snapshot),
        "snapshot_error": errorText(snapshotErr),
        "scenario": merged.Summary(),
        "replay": replay.Summary(),
        "replay_events": replay.Events(),
        "audit": audit,
        "audit_failures": audit.FailedChecks(),
        "audit_map": audit.CheckMap(),
        "reachability": reachability,
        "interlocks": l.Interlocks.Snapshot(),
        "metrics": l.Metrics.Snapshot(),
        "spans": l.Tracer.Spans(),
    }
}

func errorText(err error) string {
    if err == nil {
        return ""
    }
    return err.Error()
}

var diagnosticsOnce sync.Once
var diagnosticsValue map[string]any

func BuildDiagnostics() map[string]any {
    diagnosticsOnce.Do(func() {
        directory := filepath.Join(os.TempDir(), "wavelab-runtime")
        laboratory, err := NewLaboratory(directory)
        if err != nil {
            diagnosticsValue = map[string]any{"error": err.Error()}
            return
        }
        diagnosticsValue = laboratory.Exercise(time.Unix(1700000000, 0))
    })
    return diagnosticsValue
}
