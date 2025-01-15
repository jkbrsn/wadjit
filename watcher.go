package wadjit

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-multierror"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
)

type Watcher interface {
	Close() error
	ID() xid.ID
	Job() taskman.Job
	SetUp() error
}

type HTTPWatcher struct {
	id      xid.ID
	cadence time.Duration

	endpoints []Endpoint
	header    http.Header

	payload []byte
}

func (w *HTTPWatcher) Close() error {
	return nil
}

func (w *HTTPWatcher) ID() xid.ID {
	return w.id
}

func (w *HTTPWatcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.endpoints))
	for _, endpoint := range w.endpoints {
		tasks = append(tasks, HTTPRequest{
			Method: http.MethodGet,
			URL:    endpoint.URL,
			Header: w.header,
			Data:   w.payload,
		})
	}
	job := taskman.Job{
		ID:      w.id.String(),
		Cadence: w.cadence,
		Tasks:   tasks,
	}
	return job
}

func (w *HTTPWatcher) SetUp() error {
	return nil
}

type WSWatcher struct {
	id      xid.ID
	cadence time.Duration

	connections map[Endpoint]*websocket.Conn // TODO: make sync.Map?
	header      http.Header

	msg []byte
}

func (w *WSWatcher) Close() error {
	var result *multierror.Error
	for _, conn := range w.connections {
		err := conn.Close()
		if err != nil {
			result = multierror.Append(result, err)
		}
	}
	return result.ErrorOrNil()
}

func (w *WSWatcher) ID() xid.ID {
	return w.id
}

func (w *WSWatcher) Job() taskman.Job {
	tasks := make([]taskman.Task, 0, len(w.connections))
	for endpoint, conn := range w.connections {
		tasks = append(tasks, WSWrite{
			URL:        endpoint.URL,
			Message:    w.msg,
			Connection: conn,
		})
	}
	job := taskman.Job{
		ID:      w.id.String(),
		Cadence: w.cadence,
		Tasks:   tasks,
	}
	return job
}

func (w *WSWatcher) SetUp() error {
	var result *multierror.Error
	for endpoint := range w.connections {
		newConn, _, err := websocket.DefaultDialer.Dial(endpoint.URL.Host, w.header)
		if err != nil {
			result = multierror.Append(result, err)
		}
		w.connections[endpoint] = newConn
	}
	return result.ErrorOrNil()
}

type HTTPRequest struct {
	Header http.Header
	Method string
	URL    *url.URL
	Data   []byte
}

func (r HTTPRequest) Execute() error {
	response, err := http.DefaultClient.Do(&http.Request{
		Method: r.Method,
		URL:    r.URL,
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}
	return nil
}

type WSWrite struct {
	Connection *websocket.Conn
	Message    []byte
	URL        *url.URL
}

func (w WSWrite) Execute() error {
	err := w.Connection.WriteMessage(websocket.TextMessage, w.Message)
	if err != nil {
		return err
	}
	return nil
}
