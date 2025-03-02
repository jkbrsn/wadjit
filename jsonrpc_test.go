package wadjit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helpers

// errReader is a simple io.Reader that always returns an error
type errReader string

func (e errReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("%s", string(e))
}

//
// JSON RPC Request
//

func TestJSONRPCRequest_IsEmpty(t *testing.T) {
	t.Run("Nil receiver => true", func(t *testing.T) {
		var req *JSONRPCRequest
		assert.True(t, req.IsEmpty())
	})

	t.Run("Empty => true", func(t *testing.T) {
		req := &JSONRPCRequest{}
		assert.True(t, req.IsEmpty())
	})

	t.Run("Empty method => true", func(t *testing.T) {
		req := &JSONRPCRequest{Method: ""}
		assert.True(t, req.IsEmpty())
	})

	t.Run("Non-empty method => false", func(t *testing.T) {
		req := &JSONRPCRequest{Method: "testMethod"}
		assert.False(t, req.IsEmpty())
	})
}

func TestJSONRPCRequest_UnmarshalJSON(t *testing.T) {
	t.Run("Valid JSON with int ID", func(t *testing.T) {
		data := []byte(`{"jsonrpc":"2.0","method":"test","params":["0x123"],"id":99}`)
		expected := JSONRPCRequest{JSONRPC: "2.0", Method: "test", Params: []any{"0x123"}, ID: int64(99)}

		var result JSONRPCRequest
		err := result.UnmarshalJSON(data)
		assert.NoError(t, err, "Unexpected error")
		assert.Equal(t, expected.JSONRPC, result.JSONRPC)
		assert.Equal(t, expected.Method, result.Method)
		assert.Equal(t, expected.Params, result.Params)
		assert.Equal(t, expected.ID, result.ID)
	})

	t.Run("Valid JSON with string ID", func(t *testing.T) {
		data := []byte(`{"jsonrpc":"2.0","method":"eth_getBalance","params":[],"id":"abc"}`)
		expected := JSONRPCRequest{JSONRPC: "2.0", Method: "eth_getBalance", ID: "abc"}

		var result JSONRPCRequest
		err := result.UnmarshalJSON(data)
		assert.NoError(t, err, "Unexpected error")
		assert.Equal(t, expected.JSONRPC, result.JSONRPC)
		assert.Equal(t, expected.Method, result.Method)
		assert.Empty(t, result.Params)
		assert.Equal(t, expected.ID, result.ID)
	})

	t.Run("No ID => random assigned", func(t *testing.T) {
		data := []byte(`{"jsonrpc":"2.0","method":"eth_gasPrice"}`)
		var req JSONRPCRequest
		err := req.UnmarshalJSON(data)
		require.NoError(t, err)
		assert.NotNil(t, req.ID)
		// We can't predict the random ID, but ensure it's not empty
		assert.NotEqual(t, "", req.ID)
		assert.Equal(t, "eth_gasPrice", req.Method)
	})

	t.Run("Empty string ID => replaced with random", func(t *testing.T) {
		data := []byte(`{"jsonrpc":"2.0","id":"","method":"eth_chainId"}`)
		var req JSONRPCRequest
		err := req.UnmarshalJSON(data)
		require.NoError(t, err)
		assert.NotNil(t, req.ID)
		// If empty string, is still replaced with random int ID
		_, ok := req.ID.(string)
		assert.False(t, ok)
		_, ok = req.ID.(int64)
		assert.True(t, ok)
		assert.Equal(t, "eth_chainId", req.Method)
	})

	t.Run("Invalid JSONRPC => error", func(t *testing.T) {
		invalidJSONs := [][]byte{
			[]byte(`{"json":"2.0","id":1,"method":"eth_chainId"`),                            // Invalid JSONRPC field
			[]byte(`{"jsonrpc":"2.0","id":,"method":"eth_chainId"}`),                         // Invalid ID: missing value
			[]byte(`{"jsonrpc":"2.0","id":true,"method":"eth_chainId"}`),                     // Invalid ID: boolean
			[]byte(`{"jsonrpc":"2.0","id":{},"method":"eth_chainId"}`),                       // Invalid ID: object
			[]byte(`{"jsonrpc":"2.0","id":[],"method":"eth_chainId"}`),                       // Invalid ID: array
			[]byte(`{"jsonrpc":"2.0","id":1,"method":15`),                                    // Invalid method: number
			[]byte(`{"jsonrpc":"2.0","id":1,"method":""}`),                                   // Invalid method: empty string
			[]byte(`{"jsonrpc":"2.0","id":1,"method":{}}`),                                   // Invalid method: object
			[]byte(`{"jsonrpc":"2.0","id":1,"method":[]}`),                                   // Invalid method: array
			[]byte(`{"jsonrpc":"2.0","id":1,"method":true}`),                                 // Invalid method: boolean
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[{"nested":}]}`), // Invalid params: nested invalid JSON
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":"not_array"}`),   // Invalid params: simple string
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":15}`),            // Invalid params: number
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":}`),              // Invalid params: missing value
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":true}`),          // Invalid params: boolean
			[]byte(`{"jsonrpc":"2.0","id":1,"params":[]}`),                                   // Missing method
			[]byte(`{"jsonrpc":"2.0","id":1}`),                                               // Missing method field + params
			[]byte(`{"0x123": "abs"}`),                                                       // Invalid JSONRPC request, but valid JSON
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]`),             // Missing closing bracket
			[]byte(`{}`), // Empty JSON object
			[]byte(``),   // Empty string
		}

		for _, data := range invalidJSONs {
			var req JSONRPCRequest
			err := req.UnmarshalJSON(data)
			assert.Error(t, err, "should fail to unmarshal invalid JSON: %s", data)
		}
	})

	t.Run("Valid JSONRPC => no error", func(t *testing.T) {
		validJSONs := [][]byte{
			[]byte(`{"jsonrpc":"2.0","id":"one","method":"eth_chainId"}`),                                                   // string id
			[]byte(`{"jsonrpc":"2.0","id":1.1,"method":"eth_chainId"}`),                                                     // float id
			[]byte(`{"jsonrpc":"2.0","id":null,"method":"eth_chainId"}`),                                                    // null id
			[]byte(`{"jsonrpc":"2.0","method":"eth_chainId","params":[]}`),                                                  // No ID (notification)
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","extra":"field"}`),                                       // Extra field
			[]byte(`{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}`),                                       // Empty list params
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":{}}`),                                           // Empty object params
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":{"key": "value"}}`),                             // Object params
			[]byte(`{"jsonrpc":"2.0","id":3,"method":"eth_getBalance","params":["0x123456", "latest"]}`),                    // Multiple params
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":[{"address": "0x123", "block": "latest"}]}`), // Nested params
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`),                                                       // Missing params
			[]byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":null}`),                                         // Null params
		}

		for _, data := range validJSONs {
			var req JSONRPCRequest
			err := req.UnmarshalJSON(data)
			assert.NoError(t, err, "should successfully unmarshal valid JSON: %s", data)
		}
	})
}

func TestJSONRPCRequestFromBytes(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		data := []byte(`{"jsonrpc":"2.0","id":1,"method":"testMethod","params":["0x123"]}`)
		req, err := JSONRPCRequestFromBytes(data)
		require.NoError(t, err)
		require.NotNil(t, req)
		assert.Equal(t, "testMethod", req.Method)
		assert.EqualValues(t, 1, req.ID)
		assert.Equal(t, []any{"0x123"}, req.Params)
	})

	t.Run("Unmarshal error", func(t *testing.T) {
		// Invalid JSON, ID is empty
		data := []byte(`{"jsonrpc":"2.0","id":,"method":"testMethod"}`)
		req, err := JSONRPCRequestFromBytes(data)
		require.Error(t, err)
		require.Nil(t, req)
	})

	t.Run("Empty input data", func(t *testing.T) {
		req, err := JSONRPCRequestFromBytes([]byte{})
		require.Error(t, err)
		require.Nil(t, req)
	})
}

//
// JSON RPC Response
//

func TestJSONRPCResponse_ID(t *testing.T) {
	t.Run("No ID set => returns nil", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		assert.Nil(t, resp.ID())
	})

	t.Run("SetID => returns correct ID and caches bytes", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		err := resp.SetID("my-unique-id")
		require.NoError(t, err, "SetID should succeed for string")

		// Reading the ID should return the same 'any' value
		got := resp.ID()
		require.NotNil(t, got)
		idStr, ok := got.(string)
		require.True(t, ok)
		assert.Equal(t, "my-unique-id", idStr)
	})

	t.Run("ID loaded from idBytes if not cached", func(t *testing.T) {
		resp := &JSONRPCResponse{
			idBytes: []byte(`123`), // Set directly to simulate parse
		}
		// The first call to ID() should unmarshal idBytes
		got := resp.ID()
		require.NotNil(t, got)
		idVal, ok := got.(float64)
		require.True(t, ok, "unmarshalled type might be float64 or int depending on Sonic/JSON")
		assert.EqualValues(t, 123, idVal)
	})

	t.Run("ID unmarshal error => logs error, returns nil", func(t *testing.T) {
		resp := &JSONRPCResponse{
			idBytes: []byte(`{invalid json`),
		}
		got := resp.ID()
		assert.Nil(t, got, "on parse error, ID should end up nil")
	})
}

func TestJSONRPCResponse_IsEmpty(t *testing.T) {
	t.Run("Nil receiver => true", func(t *testing.T) {
		var resp *JSONRPCResponse
		assert.True(t, resp.IsEmpty())
	})

	t.Run("Empty => true", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		assert.True(t, resp.IsEmpty())
	})

	t.Run("Various special results => true", func(t *testing.T) {
		cases := [][]byte{
			[]byte(`"0x"`),
			[]byte(`null`),
			[]byte(`""`),
			[]byte(`[]`),
			[]byte(`{}`),
		}
		for _, c := range cases {
			resp := &JSONRPCResponse{Result: c}
			assert.True(t, resp.IsEmpty(), "expected %q to be IsEmpty == true", c)
		}
	})

	t.Run("Non-empty => false", func(t *testing.T) {
		resp := &JSONRPCResponse{Result: []byte(`"some-value"`)}
		assert.False(t, resp.IsEmpty())
	})
}

func TestJSONRPCResponse_IsNull(t *testing.T) {
	t.Run("Nil receiver => true", func(t *testing.T) {
		var resp *JSONRPCResponse
		assert.True(t, resp.IsNull())
	})

	t.Run("Empty everything => true", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		assert.True(t, resp.IsNull())
	})

	t.Run("If ID is non-zero => false", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		_ = resp.SetID(1)
		assert.False(t, resp.IsNull(), "ID is set => not null")
	})

	t.Run("If Error is non-nil => false", func(t *testing.T) {
		resp := &JSONRPCResponse{Error: &JSONRPCError{Code: 123}}
		assert.False(t, resp.IsNull(), "Error => not null")
	})

	t.Run("If Result is non-empty => false", func(t *testing.T) {
		resp := &JSONRPCResponse{Result: []byte(`"hello"`)}
		assert.False(t, resp.IsNull(), "non-empty result => not null")
	})
}

func TestJSONRPCResponse_ParseError(t *testing.T) {
	t.Run("Empty or 'null' => sets generic error", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		err := resp.ParseError("")
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		assert.Equal(t, -32603, resp.Error.Code)
		assert.Contains(t, resp.Error.Message, "unexpected empty response")

		resp2 := &JSONRPCResponse{}
		err = resp2.ParseError("null")
		require.NoError(t, err)
		assert.NotNil(t, resp2.Error)
		assert.Equal(t, -32603, resp2.Error.Code)
	})

	t.Run("Well-formed JSON-RPC error => sets fields", func(t *testing.T) {
		raw := `{"code": -32000, "message": "some error", "data": "details"}`
		resp := &JSONRPCResponse{}
		err := resp.ParseError(raw)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		assert.Equal(t, -32000, resp.Error.Code)
		assert.Equal(t, "some error", resp.Error.Message)
		assert.Equal(t, "details", resp.Error.Data)
	})

	t.Run("Case numerics => code, message, data from partial JSON", func(t *testing.T) {
		raw := `{"code":123,"message":"test msg"}`
		resp := &JSONRPCResponse{}
		err := resp.ParseError(raw)
		require.NoError(t, err)
		require.NotNil(t, resp.Error)
		assert.Equal(t, 123, resp.Error.Code)
		assert.Equal(t, "test msg", resp.Error.Message)
		assert.Nil(t, resp.Error.Data) // not provided => nil
	})

	t.Run("Case with only 'error' field => sets code -32603, message from error field", func(t *testing.T) {
		raw := `{"error": "this is an error string"}`
		resp := &JSONRPCResponse{}
		err := resp.ParseError(raw)
		require.NoError(t, err)
		assert.NotNil(t, resp.Error)
		assert.Equal(t, -32603, resp.Error.Code)
		assert.Equal(t, "this is an error string", resp.Error.Message)
	})

	t.Run("Fallback => raw is message, code -32603", func(t *testing.T) {
		raw := `some-non-json-or-other`
		resp := &JSONRPCResponse{}
		err := resp.ParseError(raw)
		require.NoError(t, err)
		assert.NotNil(t, resp.Error)
		assert.Equal(t, -32603, resp.Error.Code)
		assert.Equal(t, "some-non-json-or-other", resp.Error.Message)
	})
}

func TestJSONRPCResponse_ParseFromBytes(t *testing.T) {
	cases := []struct {
		name       string
		bytes      []byte
		runtimeErr bool
		errMessage string
		respErr    *JSONRPCError
		respID     any
		respRes    json.RawMessage
	}{
		{
			name:       "Has id and result",
			bytes:      []byte(`{"jsonrpc":"2.0","id":1,"result":{"foo":"bar"}}`),
			runtimeErr: false,
			respID:     float64(1),
			respRes:    []byte(`{"foo":"bar"}`),
		},
		{
			name:       "Has id and correctly formed error 1",
			bytes:      []byte(`{"jsonrpc":"2.0","id":"abc","error":{"code":-123,"message":"some msg"}}`),
			runtimeErr: false,
			respErr: &JSONRPCError{
				Code:    -123,
				Message: "some msg",
			},
			respID: "abc",
		},
		{
			name:       "Has id and correctly formed error 2",
			bytes:      []byte(`{"jsonrpc":"2.0","id":5,"error":{"code":-1234,"data":"some data"}}`),
			runtimeErr: false,
			respErr: &JSONRPCError{
				Code: -1234,
				Data: "some data",
			},
			respID: float64(5),
		},
		{
			name:       "Has id and error with error string",
			bytes:      []byte(`{"jsonrpc":"2.0","id":"abc","error":{"error":"some string"}}`),
			runtimeErr: false,
			respErr: &JSONRPCError{
				Code:    ServerSideException,
				Message: "some string",
			},
			respID: "abc",
		},
		{
			name:       "Has id and malformed error",
			bytes:      []byte(`{"jsonrpc":"2.0","id":"abc","error":"just a string"}`),
			runtimeErr: false,
			respErr: &JSONRPCError{
				Code:    ServerSideException,
				Message: `"just a string"`,
			},
			respID: "abc",
		},
		{
			name:       "Neither error nor result",
			bytes:      []byte(`{"jsonrpc":"2.0","id":2}`),
			runtimeErr: true,
			errMessage: "response must contain either result or error",
		},
		{
			name:       "Both error and result",
			bytes:      []byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-123,"message":"some msg"},"result":{"foo":"bar"}}`),
			runtimeErr: true,
			errMessage: "response must not contain both result and error",
		},
		{
			name:       "Invalid JSON",
			bytes:      []byte(`{invalid-json`),
			runtimeErr: true,
			errMessage: "invalid char",
		},
		{
			name:       "Empty JSON",
			bytes:      []byte{},
			runtimeErr: true,
			errMessage: "input json is empty",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &JSONRPCResponse{}
			err := resp.ParseFromBytes(c.bytes)
			if c.runtimeErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.errMessage)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, c.respErr, resp.Error)
				assert.Equal(t, c.respID, resp.ID())
				assert.Equal(t, c.respRes, resp.Result)
			}
		})
	}
}

func TestJSONRPCResponse_ParseFromStream(t *testing.T) {
	t.Run("Invalid JSON => error response", func(t *testing.T) {
		raw := []byte(`{invalid-json`)
		resp := &JSONRPCResponse{}
		err := resp.ParseFromStream(bytes.NewReader(raw), len(raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "{invalid-json")
		assert.Nil(t, resp.Error)
		assert.Nil(t, resp.Result)
	})

	t.Run("Nil reader => error return", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		err := resp.ParseFromStream(nil, 12)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot read from nil reader")
	})

	t.Run("Reader error => error return", func(t *testing.T) {
		resp := &JSONRPCResponse{}
		err := resp.ParseFromStream(errReader("some read error"), 100)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "some read error")
	})
}

func TestJSONRPCResponseFromStream(t *testing.T) {
	t.Run("Nil reader => error", func(t *testing.T) {
		resp, err := JSONRPCResponseFromStream(nil, 0)
		require.Error(t, err)
		require.Nil(t, resp)
	})

	// Content based test cases
	// TODO: more cases, and make sure the parsing actually denies malformed JSON RPC responses
	cases := []struct {
		name       string
		bytes      []byte
		runtimeErr bool
		expectErr  bool
		expectRes  string
	}{
		{
			name:       "nil bytes",
			bytes:      nil,
			runtimeErr: true,
			expectErr:  true,
			expectRes:  "",
		}, {
			name:       "valid string",
			bytes:      []byte(`{"jsonrpc":"2.0","id":42,"result":"OK"}`),
			runtimeErr: false,
			expectErr:  false,
			expectRes:  `"OK"`,
		}, {
			name:       "valid object",
			bytes:      []byte(`{"jsonrpc":"2.0","id":42,"result":{"key":"value"}}`),
			runtimeErr: false,
			expectErr:  false,
			expectRes:  `{"key":"value"}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := io.NopCloser(bytes.NewReader(c.bytes))
			resp, err := JSONRPCResponseFromStream(r, len(c.bytes))
			if c.runtimeErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, c.expectErr, resp.Error != nil)
				assert.Equal(t, c.expectRes, string(resp.Result))
			}
		})
	}
}

func TestJSONRPCResponse_SetID(t *testing.T) {
	resp := &JSONRPCResponse{}
	err := resp.SetID(1234)
	require.NoError(t, err)
	// Confirm that both id and idBytes were set
	assert.Equal(t, 1234, resp.ID())
	assert.Equal(t, []byte(`1234`), resp.idBytes)
}
