package wadjit

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stubMetricsSink struct {
	respCh  chan ResponseMetrics
	eventCh chan string
}

func newStubMetricsSink() *stubMetricsSink {
	return &stubMetricsSink{
		respCh:  make(chan ResponseMetrics, 4),
		eventCh: make(chan string, 4),
	}
}

func (s *stubMetricsSink) ObserveResponse(resp ResponseMetrics) {
	s.respCh <- resp
}

func (s *stubMetricsSink) ObserveEvent(name string, _ map[string]any) {
	s.eventCh <- name
}

func TestMetricsSinkReceivesResponses(t *testing.T) {
	sink := newStubMetricsSink()
	w := New(WithMetricsSink(sink))
	defer func() { _ = w.Close() }()

	resp := WatcherResponse{WatcherID: "w1", TaskID: "t1"}
	w.respGatherChan <- resp

	select {
	case got := <-sink.respCh:
		require.Equal(t, ResponseMetrics{
			WatcherID: resp.WatcherID,
			TaskID:    resp.TaskID,
			Target:    "",
			Times:     ResponseMetrics{}.Times, // zero value
		}, got)
	case <-time.After(time.Second):
		t.Fatal("metrics sink did not receive response")
	}
}

func TestMetricsSinkReceivesWatcherEvents(t *testing.T) {
	sink := newStubMetricsSink()
	w := New(WithMetricsSink(sink), WithMetricsSampleRate(0)) // sampling should not affect events
	defer func() { _ = w.Close() }()

	task := &MockWatcherTask{
		URL: &url.URL{Scheme: "http", Host: "example.com"},
	}
	watcher, err := NewWatcher("watcher-1", 50*time.Millisecond, []WatcherTask{task})
	require.NoError(t, err)

	require.NoError(t, w.AddWatcher(watcher))
	require.NoError(t, w.RemoveWatcher(watcher.ID))

	events := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-sink.eventCh:
			events[name] = true
		case <-time.After(time.Second):
			t.Fatalf("expected event %d not received", i+1)
		}
	}

	require.True(t, events["watcher_added"], "watcher_added not observed")
	require.True(t, events["watcher_removed"], "watcher_removed not observed")
}

func TestMetricsSinkSamplingSkipsResponsesWhenRateZero(t *testing.T) {
	sink := newStubMetricsSink()
	w := New(WithMetricsSink(sink), WithMetricsSampleRate(0))
	defer func() { _ = w.Close() }()

	resp := WatcherResponse{WatcherID: "w1", TaskID: "t1"}
	w.respGatherChan <- resp

	select {
	case got := <-sink.respCh:
		t.Fatalf("expected no sampled response, got %#v", got)
	case <-time.After(150 * time.Millisecond):
		// ok, nothing delivered
	}
}
