package service

import (
    "sort"
    "sync"
    "time"

    "github.com/wyw14/solo-037/internal/model"
)

type ScenarioEvent struct {
    ID model.EventID `json:"id"`
    Run model.RunID `json:"run"`
    Kind string `json:"kind"`
    Component string `json:"component"`
    Detail string `json:"detail"`
    At time.Time `json:"at"`
    Origin string `json:"origin,omitempty"`
    Present bool `json:"present"`
    RecordID model.EventID `json:"record_id,omitempty"`
    Segment string `json:"segment,omitempty"`
}

const (
    OriginDrill = "drill"
    OriginReal = "real"
)

type Scenario struct {
    mu sync.Mutex
    Name string `json:"name"`
    Run model.RunID `json:"run"`
    Revision model.RevisionID `json:"revision"`
    Started time.Time `json:"started"`
    Finished time.Time `json:"finished"`
    events []ScenarioEvent
}

func NewScenario(name string, run model.RunID, revision model.RevisionID, at time.Time) *Scenario {
    return &Scenario{Name: name, Run: run, Revision: revision, Started: at.UTC()}
}

func (s *Scenario) Add(kind string, component string, detail string, at time.Time) ScenarioEvent {
    s.mu.Lock()
    defer s.mu.Unlock()
    event := ScenarioEvent{ID: model.NewEventID(), Run: s.Run, Kind: kind, Component: component, Detail: detail, At: at.UTC(), Origin: OriginDrill, Present: true}
    s.events = append(s.events, event)
    return event
}

func (s *Scenario) Complete(at time.Time) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.Finished = at.UTC()
}

func (s *Scenario) Events() []ScenarioEvent {
    s.mu.Lock()
    defer s.mu.Unlock()
    result := append([]ScenarioEvent{}, s.events...)
    sort.SliceStable(result, func(i int, j int) bool { return result[i].At.Before(result[j].At) })
    return result
}

func (s *Scenario) Duration() time.Duration {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.Finished.IsZero() {
        return 0
    }
    return s.Finished.Sub(s.Started)
}

func (s *Scenario) Failed() bool {
    for _, event := range s.Events() {
        if event.Kind == "fault" || event.Kind == "rejected" {
            return true
        }
    }
    return false
}

func (s *Scenario) ComponentCounts() map[string]int {
    result := make(map[string]int)
    for _, event := range s.Events() {
        result[event.Component]++
    }
    return result
}

func (s *Scenario) KindCounts() map[string]int {
    result := make(map[string]int)
    for _, event := range s.Events() {
        result[event.Kind]++
    }
    return result
}

func (s *Scenario) MissingCount() int {
    missing := 0
    for _, event := range s.Events() {
        if !event.Present {
            missing++
        }
    }
    return missing
}

func (s *Scenario) Summary() map[string]any {
    return map[string]any{
        "name": s.Name,
        "run": s.Run,
        "revision": s.Revision,
        "origin": OriginDrill,
        "started": s.Started,
        "finished": s.Finished,
        "duration": s.Duration().String(),
        "failed": s.Failed(),
        "events": len(s.Events()),
        "missing": s.MissingCount(),
        "components": s.ComponentCounts(),
        "kinds": s.KindCounts(),
    }
}

func MergeScenarios(name string, scenarios ...*Scenario) *Scenario {
    if len(scenarios) == 0 {
        return NewScenario(name, model.NewRunID(), model.NewRevisionID(), time.Time{})
    }
    first := scenarios[0]
    merged := NewScenario(name, first.Run, first.Revision, first.Started)
    for _, scenario := range scenarios {
        for _, event := range scenario.Events() {
            merged.events = append(merged.events, event)
        }
        if scenario.Finished.After(merged.Finished) {
            merged.Finished = scenario.Finished
        }
    }
    sort.SliceStable(merged.events, func(i int, j int) bool { return merged.events[i].At.Before(merged.events[j].At) })
    return merged
}

func StandardScenario(run model.RunID, revision model.RevisionID, at time.Time) *Scenario {
    scenario := NewScenario("standard towing run", run, revision, at)
    scenario.Add("configured", "test", "model and section selected", at)
    scenario.Add("lease", "track", "exclusive track fence acquired", at.Add(time.Millisecond))
    scenario.Add("armed", "carriage", "current run command epoch active", at.Add(2*time.Millisecond))
    scenario.Add("active", "wave", "profile output started", at.Add(3*time.Millisecond))
    scenario.Add("sample", "probe", "wave height evidence captured", at.Add(4*time.Millisecond))
    scenario.Add("sample", "force", "force frame captured", at.Add(5*time.Millisecond))
    scenario.Add("durable", "journal", "measurement evidence committed", at.Add(6*time.Millisecond))
    scenario.Add("stopped", "carriage", "current brake proof accepted", at.Add(7*time.Millisecond))
    scenario.Add("settled", "absorber", "reflection window retired", at.Add(8*time.Millisecond))
    scenario.Add("complete", "test", "clearance evidence published", at.Add(9*time.Millisecond))
    scenario.Complete(at.Add(10 * time.Millisecond))
    return scenario
}
