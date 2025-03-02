package wadjit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
)

const ServerSideException = -32603

// JSONRPCError represents a standard JSON RPC error.
type JSONRPCError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`

	// Errors might contain additional data, e.g. revert reason
	Data any `json:"data,omitempty"`
}

// JSONRPCRequest is a struct for JSON RPC requests.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc,omitempty"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// JSONRPCResponse is a struct for JSON RPC responses.
type JSONRPCResponse struct {
	id      any
	idBytes []byte
	muID    sync.RWMutex

	Error    *JSONRPCError
	errBytes []byte
	muErr    sync.RWMutex

	Result   json.RawMessage
	muResult sync.RWMutex
}

// jsonRPCResponse is an internal representation of a JSON RPC response.
// This is decoupled from the public struct to allow for custom handling of the response data,
// separately from how it is marshaled and unmarshaled.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
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
	// Define an auxiliary struct that maps directly to the JSON RPC request structure
	type jsonRPCRequestAux struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}

	var aux jsonRPCRequestAux
	if err := sonic.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.JSONRPC != "2.0" {
		return fmt.Errorf("invalid jsonrpc version: expected \"2.0\", got %q", aux.JSONRPC)
	}
	r.JSONRPC = aux.JSONRPC

	if aux.Method == "" {
		return errors.New("missing required field: method")
	}
	r.Method = aux.Method

	// Unmarshal and validate the id field
	if len(aux.ID) > 0 {
		var id any
		if err := sonic.Unmarshal(aux.ID, &id); err != nil {
			return fmt.Errorf("invalid id field: %w", err)
		}
		// If the value is "null", id will be nil
		if id == nil {
			r.ID = nil
		} else {
			switch v := id.(type) {
			case float64:
				r.ID = int64(v)
			case string:
				if v == "" {
					r.ID = nil
				} else {
					r.ID = v
				}
			default:
				return errors.New("id field must be a string or a number")
			}
		}
	}
	if r.ID == nil {
		// Set an ID to treat this as a call even though it might be a notification,
		// as the ID field is required for some downstream services.
		r.ID = randomJSONRPCID()
	}

	// Unmarshal the params field
	if len(aux.Params) > 0 {
		var rawParams any
		if err := sonic.Unmarshal(aux.Params, &rawParams); err != nil {
			return fmt.Errorf("invalid params field: %w", err)
		}
		// Accept only arrays or objects.
		switch rawParams.(type) {
		case []any, map[string]any, nil:
			r.Params = rawParams
		default:
			return errors.New("params field must be either an array, an object, or nil")
		}
	} else {
		// You may choose to set this to nil or an empty value.
		r.Params = nil
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
func (r *JSONRPCResponse) ID() any {
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

	// Clear previously stored error bytes
	r.errBytes = nil

	// Trim whitespace and check for null
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		r.Error = &JSONRPCError{
			Code:    ServerSideException,
			Message: "unexpected empty response from upstream endpoint",
			Data:    "",
		}
		return nil
	}

	// 1. Unmarshal the error as a typical JSON RPC error
	var rpcErr JSONRPCError
	if err := sonic.UnmarshalString(raw, &rpcErr); err == nil {
		// If at least one of Code or Message is set, consider a valid error
		if rpcErr.Code != 0 || rpcErr.Message != "" {
			r.Error = &rpcErr
			r.errBytes = str2Mem(raw)
			return nil
		}
	}

	// 2. Unmarshal an error with numeric code, message, and data
	numericError := struct {
		Code    int    `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
		Data    string `json:"data,omitempty"`
	}{}
	if err := sonic.UnmarshalString(raw, &numericError); err == nil {
		if numericError.Code != 0 || numericError.Message != "" || numericError.Data != "" {
			r.Error = &JSONRPCError{
				Code:    numericError.Code,
				Message: numericError.Message,
				Data:    numericError.Data,
			}
			return nil
		}
	}

	// 3. Unmarshal an error providing only the "error" field as a string
	errorStrWrapper := struct {
		Error string `json:"error"`
	}{}
	if err := sonic.UnmarshalString(raw, &errorStrWrapper); err == nil && errorStrWrapper.Error != "" {
		r.Error = &JSONRPCError{
			Code:    ServerSideException,
			Message: errorStrWrapper.Error,
		}
		return nil
	}

	// 4. Fallback: if none of the above cases match, treat the raw string as the error message
	r.Error = &JSONRPCError{
		Code:    ServerSideException,
		Message: raw,
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

	return r.ParseFromBytes(data)
}

// ParseFromBytes parses a JSON RPC response from a byte slice.
func (r *JSONRPCResponse) ParseFromBytes(data []byte) error {
	// Define an auxiliary struct that maps directly to the JSON RPC response structure
	type jsonRPCResponseAux struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   json.RawMessage `json:"error,omitempty"`
	}

	var aux jsonRPCResponseAux
	if err := sonic.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Validate jsonrpc version
	if aux.JSONRPC != "2.0" {
		return errors.New("invalid jsonrpc version")
	}

	// Validate that either result or error is present
	resultExists := len(aux.Result) > 0
	errorExists := len(aux.Error) > 0

	if !resultExists && !errorExists {
		return errors.New("response must contain either result or error")
	}
	if resultExists && errorExists {
		return errors.New("response must not contain both result and error")
	}

	// Process the id field
	r.muID.Lock()
	r.idBytes = aux.ID
	r.muID.Unlock()

	// Assign result or error accordingly
	if aux.Result != nil {
		r.muResult.Lock()
		r.Result = aux.Result
		r.muResult.Unlock()
	} else {
		if err := r.ParseError(string(aux.Error)); err != nil {
			return err
		}
	}

	return nil
}

// SetID sets the ID of the JSON RPC response, as interface and as bytes.
func (r *JSONRPCResponse) SetID(id any) error {
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
	if reader == nil {
		return nil, errors.New("cannot read from nil reader")
	}

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
