package lsp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.lsp.dev/protocol"
)

type failingDirectedBatchServer struct {
	protocol.Server

	mu         sync.Mutex
	started    int
	canceled   int
	allStarted chan struct{}
}

func (s *failingDirectedBatchServer) IncomingCalls(
	ctx context.Context, params *protocol.CallHierarchyIncomingCallsParams,
) ([]protocol.CallHierarchyIncomingCall, error) {
	s.mu.Lock()
	s.started++
	if s.started == maxConcurrentCallHierarchyRequests {
		close(s.allStarted)
	}
	s.mu.Unlock()

	if params.Item.Name == "failure" {
		<-s.allStarted
		return nil, errors.New("directed failure")
	}

	<-ctx.Done()
	s.mu.Lock()
	s.canceled++
	s.mu.Unlock()
	return nil, fmt.Errorf("wait for batch cancellation: %w", ctx.Err())
}

func TestDirectedCallBatchCancelsEveryPeerAfterFailure(t *testing.T) {
	server := &failingDirectedBatchServer{allStarted: make(chan struct{})}
	items := []protocol.CallHierarchyItem{
		{Name: "failure"},
		{Name: "peer-1"},
		{Name: "peer-2"},
		{Name: "peer-3"},
	}
	request := func(
		ctx context.Context, item protocol.CallHierarchyItem, direction CallDirection,
	) (directedCallResponse, error) {
		if direction != CallDirectionIncoming {
			return directedCallResponse{}, errors.New("unexpected direction")
		}
		calls, err := server.IncomingCalls(
			ctx, &protocol.CallHierarchyIncomingCallsParams{Item: item})
		if err != nil {
			return directedCallResponse{}, fmt.Errorf("incoming calls: %w", err)
		}
		return directedCallResponse{incoming: calls}, nil
	}

	_, err := requestDirectedCallBatch(
		context.Background(), request, items, CallDirectionIncoming)
	if err == nil || !strings.Contains(err.Error(), "directed failure") {
		t.Fatalf("batch error = %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.canceled != 3 {
		t.Fatalf("canceled peers = %d, want 3", server.canceled)
	}
}
