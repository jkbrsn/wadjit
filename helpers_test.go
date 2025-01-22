package wadjit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/gorilla/websocket"
)

// upgrader is used to upgrade the connection to a WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// In tests, you may want to allow any origin, or be more restrictive depending on your scenario.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func echoServer() *httptest.Server {
	// Create a test server with a custom handler.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			// Handle WebSocket upgrade.
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
			// Echo payload back to the client
			w.WriteHeader(http.StatusOK)
			w.Write(payload)
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
