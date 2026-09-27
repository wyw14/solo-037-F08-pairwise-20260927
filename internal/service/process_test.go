package service

import (
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func advanceStage(t *testing.T, runtime *Runtime, id model.RunID, stage model.RunStage) {
	t.Helper()
	if _, err := runtime.AdvanceMeasuredRun(id, stage, time.Time{}); err != nil {
		t.Fatalf("advance %s: %v", stage, err)
	}
}

func driveHappyRun(t *testing.T, runtime *Runtime, id model.RunID) {
	t.Helper()
	if _, err := runtime.LeaseMeasuredRun(id, 7, time.Time{}); err != nil {
		t.Fatal(err)
	}
	advanceStage(t, runtime, id, model.StageArming)
	if _, err := runtime.RecordMeasuredCommand(id, CommandInput{Epoch: 1, Fence: 7, Setpoint: 1.4}); err != nil {
		t.Fatal(err)
	}
	advanceStage(t, runtime, id, model.StageAccelerating)
	advanceStage(t, runtime, id, model.StageMeasuring)
	now := time.Now().UTC()
	if _, err := runtime.RecordMeasurement(id, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.1, RecordedAt: now.Add(-2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordMeasurement(id, MeasurementInput{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12.5, RecordedAt: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	advanceStage(t, runtime, id, model.StageDecelerating)
	advanceStage(t, runtime, id, model.StageSettling)
	advanceStage(t, runtime, id, model.StageComplete)
}

func TestRuntimeTimelinePersistsAcrossReopen(t *testing.T) {
	directory := t.TempDir()
	runtime, err := OpenRuntime(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.CreateMeasuredRun("tow A", "hull A", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	driveHappyRun(t, runtime, run.Operation.ID)

	timeline, err := runtime.MeasuredRunTimeline(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range timeline.Segments {
		if !segment.Present {
			t.Fatalf("segment %q missing after drive: %s", segment.Key, segment.Gap)
		}
	}
	if timeline.Finished.IsZero() {
		t.Fatal("finished not set")
	}

	reloaded, err := OpenRuntime(directory)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := reloaded.MeasuredRunTimeline(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range rebuilt.Segments {
		if !segment.Present {
			t.Fatalf("segment %q missing after reopen: %s", segment.Key, segment.Gap)
		}
	}
	analysis, err := reloaded.AnalyzeMeasuredRun(run.Operation.ID)
	if err != nil || analysis.ForceCount != 1 || analysis.Wave.Count != 1 {
		t.Fatalf("analysis after reopen: %+v %v", analysis, err)
	}
}

func TestRuntimeRejectsInvalidProgressionWithoutJournaling(t *testing.T) {
	runtime, err := OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.CreateMeasuredRun("tow B", "hull A", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AdvanceMeasuredRun(run.Operation.ID, model.StageMeasuring, time.Time{}); err == nil {
		t.Fatal("configured -> measuring accepted")
	}
	if _, err := runtime.LeaseMeasuredRun(run.Operation.ID, 0, time.Time{}); err == nil {
		t.Fatal("zero fence accepted")
	}
	if _, err := runtime.RecordMeasuredCommand(run.Operation.ID, CommandInput{Epoch: 0, Setpoint: 1}); err == nil {
		t.Fatal("zero epoch accepted")
	}

	timeline, err := runtime.MeasuredRunTimeline(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Events) != 1 {
		t.Fatalf("rejected writes changed the journal: %d events", len(timeline.Events))
	}
	if segment := findSegment(t, timeline, "arming"); segment.Present || segment.Gap != GapPending {
		t.Fatalf("arming = %+v, want pending", segment)
	}
}

func TestRuntimeProcessEndpointsRejectUnknownRun(t *testing.T) {
	runtime, err := OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := model.NewRunID()
	if _, err := runtime.AdvanceMeasuredRun(missing, model.StageArming, time.Time{}); err == nil {
		t.Fatal("advance unknown run accepted")
	}
	if _, err := runtime.LeaseMeasuredRun(missing, 7, time.Time{}); err == nil {
		t.Fatal("lease unknown run accepted")
	}
	if _, err := runtime.RecordMeasuredCommand(missing, CommandInput{Epoch: 1, Setpoint: 1}); err == nil {
		t.Fatal("command unknown run accepted")
	}
	if _, err := runtime.MeasuredRunTimeline(missing); err == nil {
		t.Fatal("timeline unknown run accepted")
	}
}

func TestInMemoryRuntimeBuildsTimeline(t *testing.T) {
	runtime := NewRuntime()
	run, err := runtime.CreateMeasuredRun("tow C", "hull A", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := runtime.MeasuredRunTimeline(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if segment := findSegment(t, timeline, "configured"); !segment.Present {
		t.Fatal("configured segment missing in memory runtime")
	}
	if timeline.Origin != OriginReal {
		t.Fatalf("origin = %q", timeline.Origin)
	}
}

func TestDrillScenarioStaysSeparateFromRealTimeline(t *testing.T) {
	drill := StandardScenario(model.NewRunID(), model.NewRevisionID(), time.Unix(1700000000, 0).UTC())
	summary := drill.Summary()
	if summary["origin"] != OriginDrill {
		t.Fatalf("drill origin = %v", summary["origin"])
	}
	if summary["missing"] != 0 {
		t.Fatalf("drill missing = %v", summary["missing"])
	}
	for _, event := range drill.Events() {
		if event.Origin != OriginDrill || !event.Present {
			t.Fatalf("drill event lost its origin marker: %+v", event)
		}
	}
}
