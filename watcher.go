package wadjit

import (
	"bytes"
	"fmt"
	"io"
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
		tasks = append(tasks, WSSend{
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
	request, err := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(r.Data))
	if err != nil {
		return err
	}

	for key, values := range r.Header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// TODO: exchange this with a channel send
	fmt.Printf("response: %s\n", body)

	return nil
}

type WSSend struct {
	Connection *websocket.Conn
	Message    []byte
	URL        *url.URL

	// TODO: flesh out this channel, e.q. create a write pump, to work around any concurrency issues
	writeChan chan []byte
}

func (w WSSend) Execute() error {
	err := w.Connection.WriteMessage(websocket.TextMessage, w.Message)
	if err != nil {
		return err
	}

	msg := make([]byte, 512)
	w.writeChan <- msg

	return nil
}
