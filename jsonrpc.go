package wadjit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
)

// JSONRPCError represents a standard JSON RPC error.
type JSONRPCError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`

	// Errors might contain additional data, e.g. revert reason
	Data interface{} `json:"data,omitempty"`
}

// JSONRPCRequest is a struct for JSON RPC requests.
type JSONRPCRequest struct {
	JSONRPC string        `json:"jsonrpc,omitempty"`
	ID      interface{}   `json:"id,omitempty"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// JSONRPCResponse is a struct for JSON RPC responses.
type JSONRPCResponse struct {
	id      interface{}
	idBytes []byte
	muID    sync.RWMutex

	Error    *JSONRPCError
	errBytes []byte
	muErr    sync.RWMutex

	Result   json.RawMessage
	muResult sync.RWMutex
	astNode  *ast.Node
}

// jsonRPCResponse is an internal representation of a JSON RPC response.
// This is decoupled from the public struct to allow for custom handling of the response data,
// separately from how it is marshaled and unmarshaled.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

//
// JSON RPC REQUEST
//

// IDString returns the ID as a string, regardless of its type.
func (r *JSONRPCRequest) IDString() string {
	switch id := r.ID.(type) {
	case string:
		return id
	case int64:
		return fmt.Sprintf("%d", id)
	default:
		return ""
	}
}

// IsEmpty returns whether the JSON RPC request can be considered empty.
func (r *JSONRPCRequest) IsEmpty() bool {
	if r == nil {
		return true
	}

	if r.Method == "" {
		return true
	}

	return false
}

// String returns a string representation of the JSON RPC request.
// Note: implements the fmt.Stringer interface.
func (r *JSONRPCRequest) String() string {
	return fmt.Sprintf("ID: %v, Method: %s", r.ID, r.Method)
}

// UnmarshalJSON unmarshals a JSON RPC request using sonic. It includes two custom actions:
// - Sets the JSON RPC version to 2.0.
// - Unmarshals the ID seprately, to handle both string and float64 types.
func (r *JSONRPCRequest) UnmarshalJSON(data []byte) error {
	type Alias JSONRPCRequest
	aux := &struct {
		*Alias
		ID json.RawMessage `json:"id,omitempty"`
	}{
		Alias: (*Alias)(r),
	}
	aux.JSONRPC = "2.0"

	if err := sonic.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Unmarshal the ID separately
	if aux.ID != nil {
		var id interface{}
		if err := sonic.Unmarshal(aux.ID, &id); err != nil {
			return err
		}
		switch v := id.(type) {
		case float64:
			r.ID = int64(v)
		case string:
			r.ID = v
		}
	}

	// Set a random ID if none is provided
	if r.ID == nil {
		r.ID = randomJSONRPCID()
	} else {
		switch id := r.ID.(type) {
		case string:
			if id == "" {
				r.ID = randomJSONRPCID()
			}
		}
	}

	return nil
}

// JSONRPCRequestFromBytes creates a JSON RPC request from a byte slice.
func JSONRPCRequestFromBytes(data []byte) (*JSONRPCRequest, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	req := &JSONRPCRequest{}
	err := req.UnmarshalJSON(data)
	if err != nil {
		return nil, err
	}
	return req, nil
}

//
// JSON RPC RESPONSE
//

// ID returns the ID of the JSON RPC response.
func (r *JSONRPCResponse) ID() interface{} {
	r.muID.RLock()

	if r.id != nil {
		r.muID.RUnlock()
		return r.id
	}
	r.muID.RUnlock()

	r.muID.Lock()
	defer r.muID.Unlock()

	if len(r.idBytes) == 0 {
		return nil
	}

	err := sonic.Unmarshal(r.idBytes, &r.id)
	if err != nil {
		return nil
	}

	return r.id
}

// IsEmpty returns whether the JSON RPC response can be considered empty.
func (r *JSONRPCResponse) IsEmpty() bool {
	if r == nil {
		return true
	}

	r.muResult.RLock()
	defer r.muResult.RUnlock()

	lnr := len(r.Result)
	if lnr == 0 ||
		(lnr == 4 && r.Result[0] == '"' && r.Result[1] == '0' && r.Result[2] == 'x' && r.Result[3] == '"') ||
		(lnr == 4 && r.Result[0] == 'n' && r.Result[1] == 'u' && r.Result[2] == 'l' && r.Result[3] == 'l') ||
		(lnr == 2 && r.Result[0] == '"' && r.Result[1] == '"') ||
		(lnr == 2 && r.Result[0] == '[' && r.Result[1] == ']') ||
		(lnr == 2 && r.Result[0] == '{' && r.Result[1] == '}') {
		return true
	}

	return false
}

// IsNull determines if the JSON RPC response is null.
func (r *JSONRPCResponse) IsNull() bool {
	if r == nil {
		return true
	}

	r.muResult.RLock()
	defer r.muResult.RUnlock()

	r.muErr.RLock()
	defer r.muErr.RUnlock()

	if len(r.Result) == 0 && r.Error == nil && r.ID() == nil {
		return true
	}

	return false
}

// MarshalJSON marshals a JSON RPC response into a byte slice.
func (r *JSONRPCResponse) MarshalJSON() ([]byte, error) {
	// Retrieve the id value.
	r.muID.RLock()
	id := r.id
	r.muID.RUnlock()

	// Retrieve the error value.
	r.muErr.RLock()
	errVal := r.Error
	r.muErr.RUnlock()

	// Retrieve the result. Since it is already a JSON encoded []byte,
	// we wrap it as json.RawMessage to prevent sonic from re-encoding it.
	r.muResult.RLock()
	var result json.RawMessage
	if len(r.Result) > 0 {
		result = json.RawMessage(r.Result)
	}
	r.muResult.RUnlock()

	// Build the output struct. Fields with zero values are omitted.
	out := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   errVal,
		Result:  result,
	}

	return sonic.Marshal(out)
}

// ParseError parses an error from a raw JSON RPC response.
func (r *JSONRPCResponse) ParseError(raw string) error {
	r.muErr.Lock()
	defer r.muErr.Unlock()

	r.errBytes = nil

	// First attempt to unmarshal the error as a typical JSON-RPC error
	var rpcErr JSONRPCError
	if err := sonic.UnmarshalString(raw, &rpcErr); err != nil {
		// Special case: check for non-standard error structures in the raw data
		if raw == "" || raw == "null" {
			r.Error = &JSONRPCError{
				int(-32603), // TODO: ServerSideException, use a custom error code enum
				"unexpected empty response from upstream endpoint",
				"",
			}
			return nil
		}
	}

	// Check if the error is well-formed and has necessary fields
	if rpcErr.Code != 0 || rpcErr.Message != "" {
		r.Error = &rpcErr
		r.errBytes = str2Mem(raw)
		return nil
	}

	// Handle case: numeric "code", "message", and "data"
	caseNumerics := &struct {
		Code    int    `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
		Data    string `json:"data,omitempty"`
	}{}
	if err := sonic.UnmarshalString(raw, caseNumerics); err == nil {
		if caseNumerics.Code != 0 || caseNumerics.Message != "" || caseNumerics.Data != "" {
			r.Error = &JSONRPCError{
				caseNumerics.Code,
				caseNumerics.Message,
				caseNumerics.Data,
			}

			return nil
		}
	}

	// Handle case: only "error" field as a string
	caseErrorStr := &struct {
		Error string `json:"error"`
	}{}
	if err := sonic.UnmarshalString(raw, caseErrorStr); err == nil && caseErrorStr.Error != "" {
		r.Error = &JSONRPCError{
			int(-32603), // TODO: ServerSideException, use a custom error code enum
			caseErrorStr.Error,
			"",
		}
		return nil
	}

	// Handle case: no match, treat the raw data as message string
	r.Error = &JSONRPCError{
		int(-32603), // TODO: ServerSideException, use a custom error code enum
		raw,
		"",
	}
	return nil
}

// ParseFromStream parses a JSON RPC response from a stream.
func (r *JSONRPCResponse) ParseFromStream(reader io.Reader, expectedSize int) error {
	// 16KB chunks by default
	chunkSize := 16 * 1024
	data, err := ReadAll(reader, int64(chunkSize), expectedSize)
	if err != nil {
		return err
	}

	return r.ParseFromBytes(data, expectedSize)
}

// ParseFromBytes parses a JSON RPC response from a byte slice.
func (r *JSONRPCResponse) ParseFromBytes(data []byte, expectedSize int) error {
	// Parse the JSON data into an ast.Node
	searcher := ast.NewSearcher(mem2Str(data))
	searcher.CopyReturn = false
	searcher.ConcurrentRead = false
	searcher.ValidateJSON = false

	// Extract the "id" field
	if idNode, err := searcher.GetByPath("id"); err == nil {
		if rawID, err := idNode.Raw(); err == nil {
			r.muID.Lock()
			defer r.muID.Unlock()
			r.idBytes = str2Mem(rawID)
		}
	}

	// Extract the "result" or "error" field
	if resultNode, err := searcher.GetByPath("result"); err == nil {
		if rawResult, err := resultNode.Raw(); err == nil {
			r.muResult.Lock()
			defer r.muResult.Unlock()
			r.Result = str2Mem(rawResult)
			r.astNode = &resultNode
		} else {
			return err
		}
	} else if errorNode, err := searcher.GetByPath("error"); err == nil {
		if rawError, err := errorNode.Raw(); err == nil {
			if err := r.ParseError(rawError); err != nil {
				return err
			}
		} else {
			return err
		}
	} else if err := r.ParseError(mem2Str(data)); err != nil {
		return err
	}

	return nil
}

// SetID sets the ID of the JSON RPC response, as interface and as bytes.
func (r *JSONRPCResponse) SetID(id interface{}) error {
	r.muID.Lock()
	defer r.muID.Unlock()

	r.id = id

	bytes, err := sonic.Marshal(id)
	if err != nil {
		return err
	}
	r.idBytes = bytes

	return nil
}

// String returns a string representation of the JSON RPC response.
// Note: implements the fmt.Stringer interface.
func (r *JSONRPCResponse) String() string {
	return fmt.Sprintf("ID: %v, Error: %v, Result bytes: %d", r.id, r.Error, len(r.Result))
}

// JSONRPCResponseFromStream creates a JSON RPC response from a stream.
func JSONRPCResponseFromStream(body io.ReadCloser, expectedSize int) (*JSONRPCResponse, error) {
	resp := &JSONRPCResponse{}

	if body != nil {
		err := resp.ParseFromStream(body, expectedSize)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}

	return nil, fmt.Errorf("empty body")
}

//
// HELPERS
//

// randomJSONRPCID returns a value appropriate for a JSON RPC ID field, e.g. a int64 type but
// with only 32 bits range, to avoid overflow during conversions and reading/sending to upstreams.
func randomJSONRPCID() int64 {
	return int64(rand.Intn(math.MaxInt32)) // #nosec G404
}

// ReadAll reads all data from the given reader and returns it as a byte slice.
func ReadAll(reader io.Reader, chunkSize int64, expectedSize int) ([]byte, error) {
	// 16KB buffer by default
	buffer := bytes.NewBuffer(make([]byte, 0, 16*1024))

	upperSizeLimit := 50 * 1024 * 1024 // Max limit of 50MB
	if expectedSize > 0 && expectedSize < upperSizeLimit {
		n := expectedSize - buffer.Cap()
		if n > 0 {
			buffer.Grow(n)
		}
	}

	// Read data in chunks
	for {
		n, err := io.CopyN(buffer, reader, chunkSize)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if n == 0 {
			break
		}
	}

	return buffer.Bytes(), nil
}

// mem2Str safely converts a byte slice to a string without copying the underlying data.
// This is a read-only operation, as strings are immutable in Go.
// Note: Avoid modifying the original byte slice after conversion.
func mem2Str(b []byte) string {
	return string(b)
}

// str2Mem safely converts a string to a byte slice without copying the underlying data.
// The resulting byte slice should only be used for read operations to avoid violating
// Go's string immutability guarantee.
func str2Mem(s string) []byte {
	return []byte(s)
}
