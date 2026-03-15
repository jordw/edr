package output

import (
	"encoding/json"
	"fmt"
	"os"
)

// SchemaVersion is bumped on backward-incompatible envelope changes.
const SchemaVersion = 2

// Op is a flat map of operation fields. Every op has at minimum "op_id" and "type".
// Successful ops merge the result's fields directly; failed ops have an "error" key.
type Op = map[string]any

// OpError is a structured error in the envelope.
type OpError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Candidates []any  `json:"candidates,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Envelope is the unified JSON response shape for all edr commands.
type Envelope struct {
	SchemaVersion int       `json:"schema_version"`
	OK            bool      `json:"ok"`
	Command       string    `json:"command"`
	Ops           []Op      `json:"ops"`
	Verify        any       `json:"verify"`
	Errors        []OpError `json:"errors"`
}

// NewEnvelope creates an envelope for a command.
func NewEnvelope(command string) *Envelope {
	return &Envelope{
		SchemaVersion: SchemaVersion,
		OK:            true,
		Command:       command,
		Ops:           []Op{},
		Errors:        []OpError{},
	}
}

// AddOp adds a successful operation result. The result is JSON-roundtripped
// into a flat map, then op_id and type are merged onto it. Returns an error
// if the result is not a JSON object (array, scalar, nil).
func (e *Envelope) AddOp(opID, opType string, result any) error {
	flat, err := toFlatMap(result)
	if err != nil {
		e.AddFailedOp(opID, opType, fmt.Sprintf("non-object result: %v", err))
		return err
	}
	flat["op_id"] = opID
	flat["type"] = opType
	e.Ops = append(e.Ops, flat)
	return nil
}

// AddFailedOp adds a failed operation.
func (e *Envelope) AddFailedOp(opID, opType string, errMsg string) {
	e.OK = false
	e.Ops = append(e.Ops, Op{
		"op_id": opID,
		"type":  opType,
		"error": errMsg,
	})
}

// AddSkippedOp adds an op that was not attempted due to a prior failure.
// Unlike AddFailedOp, this does not set ok=false on the envelope — the
// failure is on the gating op, not this one.
func (e *Envelope) AddSkippedOp(opID, opType, reason string) {
	e.Ops = append(e.Ops, Op{
		"op_id":  opID,
		"type":   opType,
		"status": "skipped",
		"reason": reason,
	})
}

// AddError adds a structured error to the envelope.
func (e *Envelope) AddError(code, message string) {
	e.OK = false
	e.Errors = append(e.Errors, OpError{Code: code, Message: message})
}

// SetVerify sets the verify result.
func (e *Envelope) SetVerify(v any) {
	e.Verify = v
}

// ComputeOK recalculates ok based on ops, errors, and verify.
// True only when: len(errors)==0 AND no op has "error" key AND verify (if present) has ok:true.
func (e *Envelope) ComputeOK() {
	e.OK = true
	if len(e.Errors) > 0 {
		e.OK = false
		return
	}
	for _, op := range e.Ops {
		if _, hasErr := op["error"]; hasErr {
			e.OK = false
			return
		}
	}
	// Check verify
	if m, ok := e.Verify.(map[string]any); ok {
		if vOK, exists := m["ok"].(bool); exists && !vOK {
			e.OK = false
		}
	}
}

// HasOpErrors returns true if any op has an "error" key.
func (e *Envelope) HasOpErrors() bool {
	for _, op := range e.Ops {
		if _, hasErr := op["error"]; hasErr {
			return true
		}
	}
	return false
}

// IsVerifyOnlyFailure returns true if all ops succeeded but verify failed.
func (e *Envelope) IsVerifyOnlyFailure() bool {
	if e.HasOpErrors() || len(e.Errors) > 0 {
		return false
	}
	if m, ok := e.Verify.(map[string]any); ok {
		if vOK, exists := m["ok"].(bool); exists && !vOK {
			return true
		}
	}
	return false
}

// PrintEnvelope marshals the envelope to stdout.
func PrintEnvelope(e *Envelope) {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stdout, `{"schema_version":%d,"ok":false,"command":"%s","ops":[],"verify":null,"errors":[{"code":"marshal_error","message":"%v"}]}`+"\n",
			SchemaVersion, e.Command, err)
		return
	}
	fmt.Println(string(data))
}

// ErrorEnvelope creates and prints a failed envelope with a single error.
func ErrorEnvelope(command, code, message string) {
	e := NewEnvelope(command)
	e.AddError(code, message)
	PrintEnvelope(e)
}

// toFlatMap JSON-roundtrips a value into map[string]any.
// Returns an error if the result is not a JSON object.
func toFlatMap(v any) (map[string]any, error) {
	if v == nil {
		return nil, fmt.Errorf("nil result")
	}
	// Fast path: already a map
	if m, ok := v.(map[string]any); ok {
		cp := make(map[string]any, len(m))
		for k, val := range m {
			cp[k] = val
		}
		return cp, nil
	}
	// Roundtrip through JSON
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[0] != '{' {
		return nil, fmt.Errorf("result is %T, not an object", v)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
