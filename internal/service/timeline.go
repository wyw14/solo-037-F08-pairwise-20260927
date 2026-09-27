package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wyw14/solo-037/internal/journal"
	"github.com/wyw14/solo-037/internal/model"
)

const (
	GapSkipped = "skipped"
	GapPending = "pending"
)

// TimelineMeta carries the operator-visible run description that is not part
// of the append-only process records.
type TimelineMeta struct {
	Name     string
	Run      model.RunID
	Revision model.RevisionID
	Started  time.Time
}

// TimelineSegment is one expected milestone of a run. Present segments are
// backed by persisted records; missing segments carry Gap = skipped (the run
// progressed past the milestone without a record) or pending (the run never
// reached it).
type TimelineSegment struct {
	Key       string          `json:"key"`
	Stage     model.RunStage  `json:"stage,omitempty"`
	Component string          `json:"component"`
	Detail    string          `json:"detail"`
	Present   bool            `json:"present"`
	Gap       string          `json:"gap,omitempty"`
	RecordIDs []model.EventID `json:"record_ids,omitempty"`
	At        time.Time       `json:"at"`
}

// ProcessTimeline is the pure reconstruction of a real run from its persisted
// stage, command and measurement records. It is rebuilt on every request, so
// late-arriving frames immediately fill previously missing segments.
type ProcessTimeline struct {
	Name     string            `json:"name"`
	Run      model.RunID       `json:"run"`
	Revision model.RevisionID  `json:"revision"`
	Origin   string            `json:"origin"`
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished"`
	Events   []ScenarioEvent   `json:"events"`
	Segments []TimelineSegment `json:"segments"`
}

type milestoneSpec struct {
	key       string
	stage     model.RunStage
	component string
	detail    string
	anchor    int
}

// Ordered expectations for a run. The stage rows mirror model.StageSequence();
// lease/command/wave/force are the cross-stage requirements already implied by
// the drill sequence and by test.Coordinator.Complete's durability gate.
var timelineMilestones = []milestoneSpec{
	{key: "configured", stage: model.StageConfigured, component: "test", detail: "model and section selected", anchor: 0},
	{key: "lease", component: "track", detail: "exclusive track fence acquired", anchor: 1},
	{key: "arming", stage: model.StageArming, component: "test", detail: "run armed", anchor: 1},
	{key: "command", component: "carriage", detail: "acceleration command issued", anchor: 2},
	{key: "accelerating", stage: model.StageAccelerating, component: "carriage", detail: "carriage accelerating", anchor: 2},
	{key: "measuring", stage: model.StageMeasuring, component: "wave", detail: "measurement active", anchor: 3},
	{key: "wave_frame", component: "probe", detail: "wave height frame captured", anchor: 4},
	{key: "force_frame", component: "force", detail: "force frame captured", anchor: 4},
	{key: "decelerating", stage: model.StageDecelerating, component: "carriage", detail: "carriage decelerating", anchor: 4},
	{key: "settling", stage: model.StageSettling, component: "absorber", detail: "reflection window settling", anchor: 5},
	{key: "complete", stage: model.StageComplete, component: "test", detail: "run complete", anchor: 6},
}

var stageDetails = map[model.RunStage]string{
	model.StageConfigured:   "model and section selected",
	model.StageArming:       "run armed",
	model.StageAccelerating: "carriage accelerating",
	model.StageMeasuring:    "measurement active",
	model.StageDecelerating: "carriage decelerating",
	model.StageSettling:     "reflection window settling",
	model.StageComplete:     "run complete",
	model.StageAborted:      "run aborted",
}

// ReconstructTimeline rebuilds a run timeline from its persisted records.
// Every record becomes a real event; expected milestones without a backing
// record become missing segments.
func ReconstructTimeline(meta TimelineMeta, records []journal.Record) ProcessTimeline {
	ordered := append([]journal.Record{}, records...)
	sort.SliceStable(ordered, func(i int, j int) bool {
		return ordered[i].At.Before(ordered[j].At)
	})

	timeline := ProcessTimeline{
		Name:     meta.Name,
		Run:      meta.Run,
		Revision: meta.Revision,
		Origin:   OriginReal,
		Started:  meta.Started.UTC(),
		Events:   make([]ScenarioEvent, 0, len(ordered)),
	}
	backs := make(map[string][]journal.Record)
	reached := -1
	aborted := false

	for _, record := range ordered {
		event, key := timelineEvent(record)
		timeline.Events = append(timeline.Events, event)
		if key != "" {
			backs[key] = append(backs[key], record)
		}
		if record.Kind == "stage" {
			var stage model.RunStage
			if json.Unmarshal(record.Payload, &stage) == nil {
				if stage == model.StageAborted {
					aborted = true
					timeline.Finished = record.At
				} else if stage == model.StageComplete {
					timeline.Finished = record.At
				}
				if index := model.StageIndex(stage); index > reached {
					reached = index
				}
			}
		}
	}

	timeline.Segments = make([]TimelineSegment, 0, len(timelineMilestones))
	for _, milestone := range timelineMilestones {
		segment := TimelineSegment{
			Key:       milestone.key,
			Stage:     milestone.stage,
			Component: milestone.component,
			Detail:    milestone.detail,
		}
		if backing := backs[milestone.key]; len(backing) > 0 {
			segment.Present = true
			segment.At = backing[0].At
			segment.RecordIDs = make([]model.EventID, 0, len(backing))
			for _, record := range backing {
				segment.RecordIDs = append(segment.RecordIDs, record.ID)
			}
		} else {
			switch {
			case aborted && milestone.anchor > reached:
				segment.Gap = GapPending
			case reached >= milestone.anchor:
				segment.Gap = GapSkipped
			default:
				segment.Gap = GapPending
			}
		}
		timeline.Segments = append(timeline.Segments, segment)
	}
	return timeline
}

func timelineEvent(record journal.Record) (ScenarioEvent, string) {
	event := ScenarioEvent{
		ID:       model.NewEventID(),
		Run:      record.Run,
		At:       record.At.UTC(),
		Origin:   OriginReal,
		Present:  true,
		RecordID: record.ID,
	}
	switch record.Kind {
	case "stage":
		var stage model.RunStage
		if err := json.Unmarshal(record.Payload, &stage); err != nil || (model.StageIndex(stage) < 0 && stage != model.StageAborted) {
			return corruptEvent(record, event), ""
		}
		event.Kind = string(stage)
		event.Component = "test"
		event.Detail = stageDetails[stage]
		event.Segment = string(stage)
		return event, string(stage)
	case "fence":
		var fence uint64
		if err := json.Unmarshal(record.Payload, &fence); err != nil {
			return corruptEvent(record, event), ""
		}
		event.Kind = "lease"
		event.Component = "track"
		event.Detail = fmt.Sprintf("exclusive track fence %d acquired", fence)
		event.Segment = "lease"
		if fence > 0 {
			return event, "lease"
		}
		return event, ""
	case "command":
		var command model.DeviceCommand
		if err := json.Unmarshal(record.Payload, &command); err != nil {
			return corruptEvent(record, event), ""
		}
		event.Kind = "command"
		event.Component = "carriage"
		event.Detail = fmt.Sprintf("epoch %d setpoint %.3f", command.Epoch, command.Setpoint)
		event.Segment = "command"
		return event, "command"
	case "wave_trace", "force_trace":
		var evidence model.Evidence
		if err := json.Unmarshal(record.Payload, &evidence); err != nil {
			return corruptEvent(record, event), ""
		}
		key := "wave_frame"
		event.Kind = "sample"
		if record.Kind == "wave_trace" {
			event.Component = "probe"
			event.Detail = fmt.Sprintf("wave height frame from %s", evidence.SourceID)
		} else {
			key = "force_frame"
			event.Component = "force"
			event.Detail = fmt.Sprintf("force frame from %s", evidence.SourceID)
		}
		event.Segment = key
		return event, key
	default:
		event.Kind = record.Kind
		event.Component = "journal"
		event.Detail = "unrecognized record"
		return event, ""
	}
}

func corruptEvent(record journal.Record, event ScenarioEvent) ScenarioEvent {
	event.Kind = record.Kind
	event.Component = "journal"
	event.Detail = "corrupt payload"
	event.Segment = ""
	return event
}

// Segment returns the reconstructed milestone with the given key.
func (t ProcessTimeline) Segment(key string) (TimelineSegment, bool) {
	for _, segment := range t.Segments {
		if segment.Key == key {
			return segment, true
		}
	}
	return TimelineSegment{}, false
}

// GapCounts returns missing-segment counts by gap kind.
func (t ProcessTimeline) GapCounts() map[string]int {
	result := map[string]int{GapSkipped: 0, GapPending: 0}
	for _, segment := range t.Segments {
		if !segment.Present {
			result[segment.Gap]++
		}
	}
	return result
}
