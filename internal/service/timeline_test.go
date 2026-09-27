package service

import (
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/journal"
	"github.com/wyw14/solo-037/internal/model"
)

func appendRecord(t *testing.T, store *journal.Store, record journal.Record) {
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

func evidenceRecord(t *testing.T, evidence model.Evidence, at time.Time) journal.Record {
	t.Helper()
	record, err := journal.EvidenceRecord(evidence, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func commandRecord(t *testing.T, run model.RunID, command model.DeviceCommand, at time.Time) journal.Record {
	t.Helper()
	record, err := journal.CommandRecord(run, command, at)
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

func findSegment(t *testing.T, timeline ProcessTimeline, key string) TimelineSegment {
	t.Helper()
	segment, ok := timeline.Segment(key)
	if !ok {
		t.Fatalf("segment %q missing", key)
	}
	return segment
}

func assertEventsSorted(t *testing.T, timeline ProcessTimeline) {
	t.Helper()
	for index := 1; index < len(timeline.Events); index++ {
		if timeline.Events[index].At.Before(timeline.Events[index-1].At) {
			t.Fatalf("events not sorted at index %d: %s before %s", index, timeline.Events[index].At, timeline.Events[index-1].At)
		}
	}
}

func buildHappyRunRecords(t *testing.T, run model.RunID, revision model.RevisionID, at time.Time) *journal.Store {
	store, err := journal.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := model.DeviceCommand{ID: "cmd-1", Run: run, Epoch: 1, Fence: 7, Setpoint: 1.4, IssuedAt: at.Add(3 * time.Second)}
	appendRecord(t, store, stageRecord(t, run, model.StageConfigured, at))
	appendRecord(t, store, fenceRecord(t, run, 7, at.Add(time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageArming, at.Add(2*time.Second)))
	appendRecord(t, store, commandRecord(t, run, command, at.Add(3*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageAccelerating, at.Add(4*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageMeasuring, at.Add(5*time.Second)))
	wave := model.NewEvidence(run, revision, "probe-a", model.EvidenceWaveTime, 0.1, at.Add(6*time.Second))
	appendRecord(t, store, evidenceRecord(t, wave, at.Add(6*time.Second)))
	force := model.NewEvidence(run, revision, "load-cell", model.EvidenceForce, 12.5, at.Add(7*time.Second))
	appendRecord(t, store, evidenceRecord(t, force, at.Add(7*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageDecelerating, at.Add(8*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageSettling, at.Add(9*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageComplete, at.Add(10*time.Second)))
	return store
}

func TestReconstructHappyRunMarksEverySegmentPresent(t *testing.T) {
	run := model.NewRunID()
	revision := model.NewRevisionID()
	at := time.Unix(1700000000, 0).UTC()
	store := buildHappyRunRecords(t, run, revision, at)

	timeline := ReconstructTimeline(TimelineMeta{Name: "tow A", Run: run, Revision: revision, Started: at}, store.Records())
	if timeline.Origin != OriginReal {
		t.Fatalf("origin = %q, want real", timeline.Origin)
	}
	if len(timeline.Segments) != 11 {
		t.Fatalf("segments = %d, want 11", len(timeline.Segments))
	}
	for _, segment := range timeline.Segments {
		if !segment.Present || segment.Gap != "" {
			t.Fatalf("segment %q not present: %+v", segment.Key, segment)
		}
		if len(segment.RecordIDs) == 0 {
			t.Fatalf("segment %q has no backing records", segment.Key)
		}
		if segment.At.IsZero() {
			t.Fatalf("segment %q has no timestamp", segment.Key)
		}
	}
	if len(timeline.Events) != 11 {
		t.Fatalf("events = %d, want 11", len(timeline.Events))
	}
	for _, event := range timeline.Events {
		if event.Origin != OriginReal || !event.Present || event.RecordID == "" {
			t.Fatalf("event not marked real/present/backed: %+v", event)
		}
	}
	assertEventsSorted(t, timeline)
	if !timeline.Finished.Equal(at.Add(10 * time.Second)) {
		t.Fatalf("finished = %s", timeline.Finished)
	}
	if counts := timeline.GapCounts(); counts[GapSkipped] != 0 || counts[GapPending] != 0 {
		t.Fatalf("unexpected gaps: %+v", counts)
	}
}

func TestReconstructDetectsSkippedStageAndCommand(t *testing.T) {
	run := model.NewRunID()
	at := time.Unix(1700000000, 0).UTC()
	store, _ := journal.NewStore(t.TempDir())
	appendRecord(t, store, stageRecord(t, run, model.StageConfigured, at))
	appendRecord(t, store, fenceRecord(t, run, 7, at.Add(time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageArming, at.Add(2*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageMeasuring, at.Add(3*time.Second)))

	timeline := ReconstructTimeline(TimelineMeta{Run: run, Started: at}, store.Records())
	if segment := findSegment(t, timeline, "command"); segment.Present || segment.Gap != GapSkipped {
		t.Fatalf("command = %+v, want skipped", segment)
	}
	if segment := findSegment(t, timeline, "accelerating"); segment.Present || segment.Gap != GapSkipped {
		t.Fatalf("accelerating = %+v, want skipped", segment)
	}
	for _, key := range []string{"wave_frame", "force_frame", "decelerating", "settling", "complete"} {
		if segment := findSegment(t, timeline, key); segment.Present || segment.Gap != GapPending {
			t.Fatalf("%s = %+v, want pending", key, segment)
		}
	}
}

func TestReconstructCompleteWithoutEvidenceIsSkipped(t *testing.T) {
	run := model.NewRunID()
	at := time.Unix(1700000000, 0).UTC()
	store, _ := journal.NewStore(t.TempDir())
	for index, stage := range model.StageSequence() {
		appendRecord(t, store, stageRecord(t, run, stage, at.Add(time.Duration(index)*time.Second)))
	}
	appendRecord(t, store, fenceRecord(t, run, 7, at.Add(time.Second)))
	command := model.DeviceCommand{ID: "cmd-1", Run: run, Epoch: 1, Fence: 7, IssuedAt: at.Add(2 * time.Second)}
	appendRecord(t, store, commandRecord(t, run, command, at.Add(2*time.Second)))

	timeline := ReconstructTimeline(TimelineMeta{Run: run, Started: at}, store.Records())
	for _, key := range []string{"wave_frame", "force_frame"} {
		if segment := findSegment(t, timeline, key); segment.Present || segment.Gap != GapSkipped {
			t.Fatalf("%s = %+v, want skipped", key, segment)
		}
	}
}

func TestReconstructAbortRendersLaterSegmentsPending(t *testing.T) {
	run := model.NewRunID()
	at := time.Unix(1700000000, 0).UTC()
	store, _ := journal.NewStore(t.TempDir())
	appendRecord(t, store, stageRecord(t, run, model.StageConfigured, at))
	appendRecord(t, store, fenceRecord(t, run, 7, at.Add(time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageArming, at.Add(2*time.Second)))
	command := model.DeviceCommand{ID: "cmd-1", Run: run, Epoch: 1, Fence: 7, IssuedAt: at.Add(3 * time.Second)}
	appendRecord(t, store, commandRecord(t, run, command, at.Add(3*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageAccelerating, at.Add(4*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageMeasuring, at.Add(5*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageAborted, at.Add(6*time.Second)))

	timeline := ReconstructTimeline(TimelineMeta{Run: run, Started: at}, store.Records())
	for _, segment := range timeline.Segments {
		if segment.Gap == GapSkipped {
			t.Fatalf("aborted run must not report skipped segment %q", segment.Key)
		}
	}
	for _, key := range []string{"wave_frame", "force_frame", "decelerating", "settling", "complete"} {
		if segment := findSegment(t, timeline, key); segment.Present || segment.Gap != GapPending {
			t.Fatalf("%s = %+v, want pending", key, segment)
		}
	}
	if !timeline.Finished.Equal(at.Add(6 * time.Second)) {
		t.Fatalf("finished = %s, want abort time", timeline.Finished)
	}
	foundAbort := false
	for _, event := range timeline.Events {
		if event.Kind == string(model.StageAborted) {
			foundAbort = true
		}
	}
	if !foundAbort {
		t.Fatal("abort event missing from timeline")
	}
}

func TestReconstructLateBackfillClosesSegment(t *testing.T) {
	run := model.NewRunID()
	revision := model.NewRevisionID()
	at := time.Unix(1700000000, 0).UTC()
	store, _ := journal.NewStore(t.TempDir())
	command := model.DeviceCommand{ID: "cmd-1", Run: run, Epoch: 1, Fence: 7, IssuedAt: at.Add(3 * time.Second)}
	appendRecord(t, store, stageRecord(t, run, model.StageConfigured, at))
	appendRecord(t, store, fenceRecord(t, run, 7, at.Add(time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageArming, at.Add(2*time.Second)))
	appendRecord(t, store, commandRecord(t, run, command, at.Add(3*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageAccelerating, at.Add(4*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageMeasuring, at.Add(5*time.Second)))
	force := model.NewEvidence(run, revision, "load-cell", model.EvidenceForce, 12.5, at.Add(7*time.Second))
	appendRecord(t, store, evidenceRecord(t, force, at.Add(7*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageDecelerating, at.Add(8*time.Second)))

	first := ReconstructTimeline(TimelineMeta{Run: run, Revision: revision, Started: at}, store.Records())
	if segment := findSegment(t, first, "wave_frame"); segment.Present || segment.Gap != GapSkipped {
		t.Fatalf("wave_frame before backfill = %+v, want skipped", segment)
	}

	// A wave frame arrives late and is older than the force frame.
	wave := model.NewEvidence(run, revision, "probe-a", model.EvidenceWaveTime, 0.1, at.Add(6*time.Second))
	appendRecord(t, store, evidenceRecord(t, wave, at.Add(6*time.Second)))

	rebuilt := ReconstructTimeline(TimelineMeta{Run: run, Revision: revision, Started: at}, store.Records())
	segment := findSegment(t, rebuilt, "wave_frame")
	if !segment.Present || segment.Gap != "" || !segment.At.Equal(at.Add(6*time.Second)) {
		t.Fatalf("wave_frame after backfill = %+v, want present at frame time", segment)
	}
	if segment := findSegment(t, rebuilt, "force_frame"); !segment.Present {
		t.Fatal("force frame must remain present after backfill")
	}
	assertEventsSorted(t, rebuilt)
}

func TestReconstructOutOfOrderAppendSortsEvents(t *testing.T) {
	run := model.NewRunID()
	at := time.Unix(1700000000, 0).UTC()
	store, _ := journal.NewStore(t.TempDir())
	appendRecord(t, store, stageRecord(t, run, model.StageMeasuring, at.Add(3*time.Second)))
	appendRecord(t, store, stageRecord(t, run, model.StageConfigured, at))

	timeline := ReconstructTimeline(TimelineMeta{Run: run, Started: at}, store.Records())
	assertEventsSorted(t, timeline)
	if timeline.Events[0].Kind != string(model.StageConfigured) {
		t.Fatalf("first event = %s", timeline.Events[0].Kind)
	}
}
