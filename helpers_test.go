package wadjit

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/jakobilobi/go-taskman"
	"github.com/rs/xid"
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

	id       xid.ID
	respChan chan<- WatcherResponse
}

func (m *MockWatcherTask) Close() error {
	return nil
}

func (m *MockWatcherTask) Initialize(id xid.ID, responseChan chan<- WatcherResponse) error {
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

// echoServer creates a test server that echos back the payload sent to it.
func echoServer() *httptest.Server {
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

		default:
			// Read payload from the client
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				// Write harcoded message to the client
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("no payload found"))
				return
			}
			// Echo content type header back to the client
			if r.Header.Get("Content-Type") == "application/json" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(http.StatusOK)
			// Echo payload back to the client
			w.Write(payload)
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
				var req JSONRPCRequest
				err = req.UnmarshalJSON(message)
				if err != nil {
					// Respond with a parse error
					resp := JSONRPCResponse{
						Error: &JSONRPCError{
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
				resp := JSONRPCResponse{
					id:     req.ID,
					Error:  nil,
					Result: message,
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
			var req JSONRPCRequest
			err = req.UnmarshalJSON(payload)
			if err != nil {
				// Respond with a parse error
				resp := JSONRPCResponse{
					Error: &JSONRPCError{
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
			resp := JSONRPCResponse{
				id:     req.ID,
				Error:  nil,
				Result: payload,
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
	m.Range(func(key, value interface{}) bool {
		length++
		return true
	})
	return length
}
