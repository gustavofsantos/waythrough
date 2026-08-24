package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// A valid hierarchy at the semantic limits fits comfortably below this cap.
// Keeping the directed result smaller than the general LSP frame cap prevents
// four concurrent responses from multiplying large decoder allocations.
const maxDirectedCallResultBytes = 8 << 20

type boundedIncomingCallResult struct {
	calls []protocol.CallHierarchyIncomingCall
}

func (r *boundedIncomingCallResult) UnmarshalJSON(data []byte) error {
	if err := validateDirectedResultSize(data); err != nil {
		return err
	}
	wires, err := decodeBoundedArray[incomingCallWire](
		data, maxCallHierarchyCalls+1, "calls")
	if err != nil {
		return err
	}
	r.calls = make([]protocol.CallHierarchyIncomingCall, len(wires))
	for index, wire := range wires {
		r.calls[index] = protocol.CallHierarchyIncomingCall{
			From:       wire.From.protocolItem(),
			FromRanges: wire.FromRanges,
		}
	}
	return nil
}

type boundedOutgoingCallResult struct {
	calls []protocol.CallHierarchyOutgoingCall
}

func (r *boundedOutgoingCallResult) UnmarshalJSON(data []byte) error {
	if err := validateDirectedResultSize(data); err != nil {
		return err
	}
	wires, err := decodeBoundedArray[outgoingCallWire](
		data, maxCallHierarchyCalls+1, "calls")
	if err != nil {
		return err
	}
	r.calls = make([]protocol.CallHierarchyOutgoingCall, len(wires))
	for index, wire := range wires {
		r.calls[index] = protocol.CallHierarchyOutgoingCall{
			To:         wire.To.protocolItem(),
			FromRanges: wire.FromRanges,
		}
	}
	return nil
}

type incomingCallWire struct {
	From       directedCallItem  `json:"from"`
	FromRanges boundedCallRanges `json:"fromRanges"`
}

type outgoingCallWire struct {
	To         directedCallItem  `json:"to"`
	FromRanges boundedCallRanges `json:"fromRanges"`
}

// directedCallItem omits fields that Waythrough never returns. In particular,
// Data can contain arbitrary server-owned JSON and must not inflate a result
// that only needs the symbol's display fields and location.
type directedCallItem struct {
	Name           string              `json:"name"`
	Kind           protocol.SymbolKind `json:"kind"`
	Detail         *string             `json:"detail"`
	URI            uri.URI             `json:"uri"`
	SelectionRange protocol.Range      `json:"selectionRange"`
}

func (i directedCallItem) protocolItem() protocol.CallHierarchyItem {
	return protocol.CallHierarchyItem{
		Name:           i.Name,
		Kind:           i.Kind,
		Detail:         i.Detail,
		URI:            i.URI,
		SelectionRange: i.SelectionRange,
	}
}

type boundedCallRanges []protocol.Range

func (r *boundedCallRanges) UnmarshalJSON(data []byte) error {
	ranges, err := decodeBoundedArray[protocol.Range](
		data, maxCallHierarchySites+1, "call sites")
	if err != nil {
		return err
	}
	*r = ranges
	return nil
}

func decodeBoundedArray[T any](data []byte, limit int, noun string) ([]T, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", noun, err)
	}
	if token != json.Delim('[') {
		return nil, fmt.Errorf("decode %s: expected array", noun)
	}

	values := make([]T, 0, min(limit, 16))
	for decoder.More() {
		if len(values) >= limit {
			return nil, fmt.Errorf("decoded more than %d %s", limit, noun)
		}
		var value T
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode %s item: %w", noun, err)
		}
		values = append(values, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("finish %s array: %w", noun, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("finish %s array: %w", noun, err)
	}
	return values, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing JSON value: %w", err)
	}
	return errors.New("unexpected trailing JSON value")
}

func validateDirectedResultSize(data []byte) error {
	if len(data) > maxDirectedCallResultBytes {
		return fmt.Errorf(
			"directed call result has %d bytes; maximum is %d",
			len(data), maxDirectedCallResultBytes)
	}
	return nil
}

func requestIncomingCalls(
	ctx context.Context, attempt serverAttempt, item protocol.CallHierarchyItem,
) ([]protocol.CallHierarchyIncomingCall, error) {
	params := &protocol.CallHierarchyIncomingCallsParams{Item: item}
	if attempt.conn == nil {
		calls, err := attempt.server.IncomingCalls(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("request incoming calls: %w", err)
		}
		return calls, nil
	}
	var result boundedIncomingCallResult
	if err := protocol.Call(
		ctx, attempt.conn, protocol.MethodCallHierarchyIncomingCalls, params, &result,
	); err != nil {
		return nil, fmt.Errorf("request incoming calls: %w", err)
	}
	return result.calls, nil
}

func requestOutgoingCalls(
	ctx context.Context, attempt serverAttempt, item protocol.CallHierarchyItem,
) ([]protocol.CallHierarchyOutgoingCall, error) {
	params := &protocol.CallHierarchyOutgoingCallsParams{Item: item}
	if attempt.conn == nil {
		calls, err := attempt.server.OutgoingCalls(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("request outgoing calls: %w", err)
		}
		return calls, nil
	}
	var result boundedOutgoingCallResult
	if err := protocol.Call(
		ctx, attempt.conn, protocol.MethodCallHierarchyOutgoingCalls, params, &result,
	); err != nil {
		return nil, fmt.Errorf("request outgoing calls: %w", err)
	}
	return result.calls, nil
}
