package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

type timelineResponse struct {
	Data struct {
		Origin   string `json:"origin"`
		Segments []struct {
			Key     string `json:"key"`
			Present bool   `json:"present"`
			Gap     string `json:"gap"`
		} `json:"segments"`
		Events []struct {
			Kind    string    `json:"kind"`
			Origin  string    `json:"origin"`
			Present bool      `json:"present"`
			At      time.Time `json:"at"`
		} `json:"events"`
	} `json:"data"`
}

func request(t *testing.T, handler http.Handler, method string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, reader))
	return recorder
}

func decodeTimeline(t *testing.T, recorder *httptest.ResponseRecorder) timelineResponse {
	t.Helper()
	var response timelineResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestMeasuredRunTimelineHTTPBackfillAndDrillSeparation(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouter(runtime).Handler()

	created := request(t, handler, http.MethodPost, "/api/measured-runs", `{"name":"tow A","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var createdData struct {
		Data struct {
			Operation model.Operation `json:"operation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdData); err != nil {
		t.Fatal(err)
	}
	id := string(createdData.Data.Operation.ID)
	base := "/api/measured-runs/" + id

	initial := request(t, handler, http.MethodGet, base+"/timeline", "")
	if initial.Code != http.StatusOK {
		t.Fatalf("timeline: %d %s", initial.Code, initial.Body)
	}
	timeline := decodeTimeline(t, initial)
	if timeline.Data.Origin != "real" {
		t.Fatalf("origin = %q", timeline.Data.Origin)
	}
	pending := 0
	for _, segment := range timeline.Data.Segments {
		if segment.Key == "configured" && !segment.Present {
			t.Fatal("configured must be present right after creation")
		}
		if segment.Key != "configured" && segment.Gap != "pending" {
			t.Fatalf("segment %s = present:%v gap:%q, want pending", segment.Key, segment.Present, segment.Gap)
		}
		if segment.Gap == "pending" {
			pending++
		}
	}
	if pending != 10 {
		t.Fatalf("pending = %d, want 10", pending)
	}

	steps := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, base + "/lease", `{"fence":7}`},
		{http.MethodPost, base + "/stages", `{"stage":"arming"}`},
		{http.MethodPost, base + "/commands", `{"epoch":1,"fence":7,"setpoint":1.4}`},
		{http.MethodPost, base + "/stages", `{"stage":"accelerating"}`},
		{http.MethodPost, base + "/stages", `{"stage":"measuring"}`},
	}
	older := time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339Nano)
	oldest := time.Now().Add(-3 * time.Second).UTC().Format(time.RFC3339Nano)
	steps = append(steps,
		struct{ method, path, body string }{http.MethodPost, base + "/evidence", `{"kind":"force","source_id":"load-cell","value":12.5,"recorded_at":"` + older + `"}`},
		struct{ method, path, body string }{http.MethodPost, base + "/evidence", `{"kind":"wave_time","source_id":"probe-a","value":0.1,"recorded_at":"` + oldest + `"}`},
		struct{ method, path, body string }{http.MethodPost, base + "/stages", `{"stage":"decelerating"}`},
		struct{ method, path, body string }{http.MethodPost, base + "/stages", `{"stage":"settling"}`},
		struct{ method, path, body string }{http.MethodPost, base + "/stages", `{"stage":"complete"}`},
	)
	for _, step := range steps {
		response := request(t, handler, step.method, step.path, step.body)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", step.method, step.path, response.Code, response.Body)
		}
	}

	final := request(t, handler, http.MethodGet, base+"/timeline", "")
	if final.Code != http.StatusOK {
		t.Fatalf("final timeline: %d %s", final.Code, final.Body)
	}
	timeline = decodeTimeline(t, final)
	if len(timeline.Data.Segments) != 11 {
		t.Fatalf("segments = %d", len(timeline.Data.Segments))
	}
	for _, segment := range timeline.Data.Segments {
		if !segment.Present || segment.Gap != "" {
			t.Fatalf("segment %s present:%v gap:%q", segment.Key, segment.Present, segment.Gap)
		}
	}
	var last time.Time
	for index, event := range timeline.Data.Events {
		if event.Origin != "real" || !event.Present {
			t.Fatalf("event %d not real/present: %+v", index, event)
		}
		if index > 0 && event.At.Before(last) {
			t.Fatalf("late backfilled frame is not sorted into position at event %d", index)
		}
		last = event.At
	}

	// Validation and not-found behaviour.
	if response := request(t, handler, http.MethodGet, "/api/measured-runs/missing/timeline", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown timeline = %d, want 404", response.Code)
	}
	before := decodeTimeline(t, request(t, handler, http.MethodGet, base+"/timeline", ""))
	if response := request(t, handler, http.MethodPost, base+"/stages", `{"stage":"measuring"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("illegal stage = %d, want 400", response.Code)
	}
	if response := request(t, handler, http.MethodPost, base+"/stages", `{"stage":"nonsense"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown stage = %d, want 400", response.Code)
	}
	after := decodeTimeline(t, request(t, handler, http.MethodGet, base+"/timeline", ""))
	if len(after.Data.Events) != len(before.Data.Events) {
		t.Fatalf("rejected stage changed timeline: %d vs %d", len(after.Data.Events), len(before.Data.Events))
	}

	// The fixed drill scenario stays viewable through diagnostics and keeps its origin.
	diagnostics := service.BuildDiagnostics()
	scenario, ok := diagnostics["scenario"].(map[string]any)
	if !ok {
		t.Fatalf("diagnostics scenario missing: %+v", diagnostics)
	}
	if scenario["name"] != "commissioning evidence" || scenario["origin"] != "drill" {
		t.Fatalf("drill scenario changed: %+v", scenario)
	}
}
