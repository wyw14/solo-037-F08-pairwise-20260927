package service

import (
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/journal"
	"github.com/wyw14/solo-037/internal/model"
)

func appendFatal(t *testing.T, store *journal.Store, record journal.Record) {
	t.Helper()
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
}

func stageRecord(t *testing.T, run model.RunID, stage model.RunStage, at time.Time) journal.Record {
	t.Helper()
	record, err := journal.StageRecord(run, stage, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func fenceRecord(t *testing.T, run model.RunID, fence uint64, at time.Time) journal.Record {
	t.Helper()
	record, err := journal.FenceRecord(run, fence, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func evidenceRecord(t *testing.T, evidence model.Evidence) journal.Record {
	t.Helper()
	record, err := journal.EvidenceRecord(evidence, evidence.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func commandRecord(t *testing.T, command model.DeviceCommand) journal.Record {
	t.Helper()
	record, err := journal.CommandRecord(command, command.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func buildRunJournal(t *testing.T, at time.Time) (*journal.Store, model.RunID, model.RevisionID) {
	t.Helper()
	store, err := journal.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	operation := model.NewOperation("tow replay", "hull", "section-a", "carriage-1", at)
	for index, stage := range model.StageSequence() {
		appendFatal(t, store, stageRecord(t, operation.ID, stage, at.Add(time.Duration(index)*time.Millisecond)))
	}
	wave := model.NewEvidence(operation.ID, operation.Revision, "probe-a", model.EvidenceWaveTime, 0.2, at.Add(3*time.Millisecond)).WithDurability(true)
	force := model.NewEvidence(operation.ID, operation.Revision, "load-cell", model.EvidenceForce, 12.5, at.Add(3*time.Millisecond)).WithDurability(true)
	appendFatal(t, store, evidenceRecord(t, wave))
	appendFatal(t, store, evidenceRecord(t, force))
	command := model.DeviceCommand{ID: "cmd-1", Run: operation.ID, Epoch: 1, Fence: 7, Setpoint: 1.4, IssuedAt: at.Add(time.Millisecond)}
	appendFatal(t, store, commandRecord(t, command))
	appendFatal(t, store, fenceRecord(t, operation.ID, 7, at))
	return store, operation.ID, operation.Revision
}

func TestReplayScenarioRebuildsTimelineFromPersistedRecords(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	store, run, revision := buildRunJournal(t, at)

	scenario := ReplayScenario("tow replay", run, revision, store.Records())
	events := scenario.Events()
	if len(events) != 11 {
		t.Fatalf("expected 7 stages + fence + command + 2 evidence = 11, got %d: %+v", len(events), events)
	}
	for _, event := range events {
		if event.Source != EventSourcePersisted {
			t.Fatalf("expected all events persisted, got %s for %s/%s", event.Source, event.Kind, event.Component)
		}
	}
	if len(scenario.Missing()) != 0 {
		t.Fatalf("expected no missing segments: %+v", scenario.Missing())
	}
	if !scenario.Finished.Equal(at.Add(6 * time.Millisecond)) {
		t.Fatalf("finished should follow complete record: %v", scenario.Finished)
	}
	sources := scenario.SourceCounts()
	if sources[EventSourcePersisted] != 11 || sources[EventSourceMissing] != 0 {
		t.Fatalf("unexpected source counts: %v", sources)
	}
	summary, ok := scenario.Summary()["missing"].(int)
	if !ok || summary != 0 {
		t.Fatalf("summary missing count wrong: %v", scenario.Summary()["missing"])
	}
}

func TestReplayScenarioMarksMissingStagesAndEvidence(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	store, err := journal.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	operation := model.NewOperation("gappy run", "hull", "section-a", "carriage-1", at)
	// Jump straight from configured to measuring: arming and accelerating missing.
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageConfigured, at))
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageMeasuring, at.Add(time.Millisecond)))
	// Only wave evidence; force frame missing.
	wave := model.NewEvidence(operation.ID, operation.Revision, "probe-a", model.EvidenceWaveTime, 0.2, at.Add(2*time.Millisecond)).WithDurability(true)
	appendFatal(t, store, evidenceRecord(t, wave))
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageComplete, at.Add(5*time.Millisecond)))

	scenario := ReplayScenario("gappy replay", operation.ID, operation.Revision, store.Records())
	missing := scenario.Missing()
	kinds := map[string]bool{}
	for _, event := range missing {
		kinds[event.Kind+":"+event.Component] = true
	}
	for _, expected := range []string{
		string(model.StageArming) + ":test",
		string(model.StageAccelerating) + ":test",
		string(model.StageDecelerating) + ":test",
		string(model.StageSettling) + ":test",
		"sample:force",
	} {
		if !kinds[expected] {
			t.Fatalf("expected missing segment %s, got %v", expected, kinds)
		}
	}

	events := scenario.Events()
	for index := 1; index < len(events); index++ {
		if events[index].At.Before(events[index-1].At) {
			t.Fatalf("timeline not ordered at %d", index)
		}
	}
}

func TestReplayScenarioBackfilledRecordsUpdateMissingSegments(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	store, err := journal.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	operation := model.NewOperation("backfill run", "hull", "section-a", "carriage-1", at)
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageConfigured, at))
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageMeasuring, at.Add(time.Millisecond)))
	wave := model.NewEvidence(operation.ID, operation.Revision, "probe-a", model.EvidenceWaveTime, 0.2, at.Add(2*time.Millisecond)).WithDurability(true)
	appendFatal(t, store, evidenceRecord(t, wave))
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageComplete, at.Add(5*time.Millisecond)))

	scenario := ReplayScenario("backfill replay", operation.ID, operation.Revision, store.Records())
	if len(scenario.Missing()) == 0 {
		t.Fatal("expected missing segments before backfill")
	}
	beforeCount := len(scenario.Events())

	// A drill frame layered on top must survive the later reconcile.
	scenario.Add("sample", "force", "drill force frame", at.Add(3*time.Millisecond))

	// Backfill: force frame and the settling stage arrive late.
	force := model.NewEvidence(operation.ID, operation.Revision, "load-cell", model.EvidenceForce, 11.0, at.Add(3*time.Millisecond)).WithDurability(true)
	appendFatal(t, store, evidenceRecord(t, force))
	appendFatal(t, store, stageRecord(t, operation.ID, model.StageSettling, at.Add(4*time.Millisecond)))

	rebuilt := ReplayScenario("backfill replay", operation.ID, operation.Revision, store.Records())
	for _, event := range rebuilt.Events() {
		if event.Component == "force" && event.Kind == "sample" && event.Source == EventSourceMissing {
			t.Fatal("backfilled force frame did not replace missing segment")
		}
		if event.Kind == string(model.StageSettling) && event.Source == EventSourceMissing {
			t.Fatal("backfilled settling stage did not replace missing segment")
		}
	}
	if len(rebuilt.Missing()) >= len(scenario.Missing()) {
		t.Fatalf("backfill should shrink missing segments: before=%d after=%d", len(scenario.Missing()), len(rebuilt.Missing()))
	}

	// Reconcile keeps the drill frame but refreshes every rebuilt event.
	scenario.Reconcile(rebuilt.Events(), rebuilt.Finished)
	sources := scenario.SourceCounts()
	if sources[EventSourceDrill] != 1 {
		t.Fatalf("drill events must survive reconcile: %v", sources)
	}
	if got := len(scenario.Events()); got != beforeCount+1 {
		// The backfilled settling record replaces a missing segment, so only
		// the retained drill frame grows the timeline.
		t.Fatalf("reconcile should keep the drill frame and refresh rebuilt events: before=%d after=%d", beforeCount, got)
	}
	for _, event := range scenario.Events() {
		if event.Source == EventSourceMissing && (event.Component == "force" || event.Kind == string(model.StageSettling)) {
			t.Fatalf("stale missing segment survived reconcile: %+v", event)
		}
	}
}

func TestReplayScenarioEmptyRunHasNoEvents(t *testing.T) {
	run := model.NewRunID()
	revision := model.NewRevisionID()
	scenario := ReplayScenario("empty", run, revision, nil)
	if len(scenario.Events()) != 0 || scenario.Started != (time.Time{}) {
		t.Fatalf("empty replay should contain no events: %+v", scenario.Events())
	}
}

func TestReplayScenarioAbortedStageIsFault(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	store, err := journal.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := model.NewRunID()
	revision := model.NewRevisionID()
	appendFatal(t, store, stageRecord(t, run, model.StageConfigured, at))
	appendFatal(t, store, stageRecord(t, run, model.StageArming, at.Add(time.Millisecond)))
	appendFatal(t, store, stageRecord(t, run, model.StageAborted, at.Add(2*time.Millisecond)))

	scenario := ReplayScenario("aborted", run, revision, store.Records())
	if !scenario.Failed() {
		t.Fatal("aborted stage should surface as fault")
	}
	found := false
	for _, event := range scenario.Events() {
		if event.Kind == "fault" && event.Source == EventSourcePersisted {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a persisted fault event for the aborted stage")
	}
}

func TestStandardScenarioRemainsDrillOnly(t *testing.T) {
	scenario := StandardScenario(model.NewRunID(), model.NewRevisionID(), time.Unix(1700000000, 0))
	for _, event := range scenario.Events() {
		if event.Source != EventSourceDrill {
			t.Fatalf("standard scenario must stay drill-only, got %s", event.Source)
		}
	}
	if scenario.SourceCounts()[EventSourceDrill] != 10 {
		t.Fatalf("expected 10 drill events: %v", scenario.SourceCounts())
	}
}

func TestMeasuredRunReplayMarksMissingForceAndBackfills(t *testing.T) {
	archive, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := archive.Create("operator tow", "hull", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	if _, err := archive.Record(run.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.2, RecordedAt: at}); err != nil {
		t.Fatal(err)
	}
	scenario, err := archive.Scenario(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenario.Missing()) != 1 || scenario.Missing()[0].Component != "force" {
		t.Fatalf("expected a single missing force segment: %+v", scenario.Missing())
	}

	if _, err := archive.Record(run.Operation.ID, MeasurementInput{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12, RecordedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	scenario, err = archive.Scenario(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenario.Missing()) != 0 {
		t.Fatalf("backfilled frame should close the gap: %+v", scenario.Missing())
	}
	for _, event := range scenario.Events() {
		if event.Component == "force" && event.Source != EventSourcePersisted {
			t.Fatalf("force frame must be persisted after backfill: %+v", event)
		}
	}
}

func TestMeasuredRunReplayUnknownRunErrors(t *testing.T) {
	archive, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Scenario(model.NewRunID()); err == nil {
		t.Fatal("expected error for unknown run")
	}
}
