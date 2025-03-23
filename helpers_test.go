package wadjit

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/jakobilobi/go-jsonrpc"
	"github.com/jakobilobi/go-taskman"
)

//
// Mocks
//

type MockTask struct {
	mwt     *MockWatcherTask
	Payload []byte

	respChan chan<- WatcherResponse
}

func (t *MockTask) Execute() error {
	if t.mwt.ErrTaskResponse != nil {
		t.respChan <- errorResponse(t.mwt.ErrTaskResponse, t.mwt.id, t.mwt.URL)
	} else {
		t.respChan <- WatcherResponse{
			WatcherID: t.mwt.id,
			URL:       t.mwt.URL,
			Err:       nil,
			Payload:   &MockTaskResponse{data: t.Payload},
		}
	}
	return t.mwt.ErrTaskResponse
}

type MockTaskResponse struct {
	data []byte
}

func (m *MockTaskResponse) Data() ([]byte, error) {
	return m.data, nil
}

func (m *MockTaskResponse) Metadata() TaskResponseMetadata {
	return TaskResponseMetadata{}
}

func (m *MockTaskResponse) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.data)), nil
}

type MockWatcherTask struct {
	URL     *url.URL
	Header  http.Header
	Payload []byte

	ErrTaskResponse error // Set to return a task that errors

	id       string
	respChan chan<- WatcherResponse
}

func (m *MockWatcherTask) Close() error {
	return nil
}

func (m *MockWatcherTask) Initialize(id string, responseChan chan<- WatcherResponse) error {
	m.id = id
	m.respChan = responseChan
	return nil
}

func (m *MockWatcherTask) Task() taskman.Task {
	return &MockTask{
		mwt:      m,
		Payload:  m.Payload,
		respChan: m.respChan,
	}
}

func (m *MockWatcherTask) Validate() error {
	return nil
}

func TestMockWatcherTaskImplementsWatcherTask(t *testing.T) {
	var _ WatcherTask = &MockWatcherTask{}
}

//
// Helper functions
//

// upgrader is used to upgrade the connection to a WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// In tests, you may want to allow any origin, or be more restrictive depending on your scenario.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// echoServer creates a custom handled server that echoes back the payload sent to it,
// if a payload is present.
func echoServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {

		// Handle WebSocket
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				http.Error(w, "Could not open websocket connection", http.StatusBadRequest)
				return
			}
			defer conn.Close()

			// Echo messages back to the client
			for {
				mt, message, err := conn.ReadMessage()
				if err != nil {
					return
				}
				err = conn.WriteMessage(mt, message)
				if err != nil {
					return
				}
			}

		// Handle HTTP
		default:
			switch r.Method {
			case http.MethodOptions:
				fallthrough
			case http.MethodDelete:
				fallthrough
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				w.Write(fmt.Appendf(nil, "%s request received on path %s", r.Method, r.URL.Path))

			case http.MethodPatch:
				fallthrough
			case http.MethodPost:
				fallthrough
			case http.MethodPut:
				payload, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("no payload found"))
					return
				}
				// Mirror request's content type in the response
				if _, ok := r.Header["Content-Type"]; ok {
					w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
				}
				w.WriteHeader(http.StatusOK)
				// Echo payload back to the client
				w.Write(payload)

			default:
				// Write harcoded message to the client
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("unsupported method"))
			}
		}
	}))

	return server
}

// jsonRPCServer creates a test server that responds to JSON-RPC requests.
// The server echoes back the entire message sent to it under the "result" key, and the request ID under
// the "id" key. If the payload is not a valid JSON-RPC request, the server will respond with a parse
// error as per the JSON-RPC 2.0 specification.
func jsonRPCServer() *httptest.Server {
	// Create a test server with a custom handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			// Handle WebSocket upgrade
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				http.Error(w, "Could not open websocket connection", http.StatusBadRequest)
				return
			}
			defer conn.Close()

			// Echo messages back to the client as the result of a JSON-RPC request
			for {
				mt, message, err := conn.ReadMessage()
				if err != nil {
					return
				}
				// Parse the message as a JSON-RPC request
				var req jsonrpc.Request
				err = req.UnmarshalJSON(message)
				if err != nil {
					// Respond with a parse error
					resp := jsonrpc.Response{
						JSONRPC: "2.0",
						Error: &jsonrpc.Error{
							Code:    -32700,
							Message: "Parse error",
						},
					}
					respBytes, _ := resp.MarshalJSON()
					err = conn.WriteMessage(mt, respBytes)
					if err != nil {
						return
					}
					continue
				}
				// Echo the request back to the client
				fmt.Printf("THE ID: %v", req.ID)
				resp := jsonrpc.Response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   nil,
					Result:  message,
				}
				respBytes, _ := resp.MarshalJSON()
				err = conn.WriteMessage(mt, respBytes)
				if err != nil {
					return
				}
			}

		default:
			// Read payload from the client
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				// Write harcoded message to the client
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("no payload found"))
				return
			}

			// JSON header not found
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("no Content-Type: application/json header found"))
				return
			}

			// Parse the payload as a JSON-RPC request
			var req jsonrpc.Request
			err = req.UnmarshalJSON(payload)
			if err != nil {
				// Respond with a parse error
				resp := jsonrpc.Response{
					JSONRPC: "2.0",
					Error: &jsonrpc.Error{
						Code:    -32700,
						Message: "Parse error",
					},
				}
				respBytes, _ := resp.MarshalJSON()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(respBytes)
				return
			}

			// Build the response
			resp := jsonrpc.Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   nil,
				Result:  payload,
			}
			respBytes, _ := resp.MarshalJSON()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Echo payload back to the client as the result of a JSON-RPC request
			w.Write(respBytes)
		}
	}))

	return server
}

func syncMapLen(m *sync.Map) int {
	var length int
	m.Range(func(key, value any) bool {
		length++
		return true
	})
	return length
}
