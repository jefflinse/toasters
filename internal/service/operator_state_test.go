package service

import (
	"context"
	"sync"
	"testing"
)

// TestOperatorStateConcurrentSwap exercises readers of the live-swappable
// operator state (Status, SendMessage, ListModels, and the dispatch
// accessors) concurrently with the state swap startOperator performs on
// live provider activation. Meaningful under -race: before this state moved
// into opMu-guarded fields, these readers touched unsynchronized cfg fields
// and raced PUT /api/v1/operator/provider.
func TestOperatorStateConcurrentSwap(t *testing.T) {
	svc := NewLocal(LocalConfig{})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = svc.Status(ctx)
				_, _ = svc.SendMessage(ctx, "hello")
				_, _ = svc.ListModels(ctx)
				_ = svc.currentOperator()
				_ = svc.currentProvider()
				_ = svc.currentGraphExecutor()
				_, _ = svc.currentDefaults()
				_, _, _ = svc.operatorInfo()
			}
		}()
	}

	// Mirror the state swap startOperator performs under opMu, plus the
	// public post-construction setters.
	for range 1000 {
		svc.opMu.Lock()
		svc.op = nil
		svc.opProvider = nil
		svc.opModel = "model-a"
		svc.opEndpoint = "http://localhost:1234"
		svc.defaultProvider = "prov-a"
		svc.defaultModel = "model-a"
		svc.opMu.Unlock()
		svc.SetOperator(nil)
		svc.SetGraphExecutor(nil)
	}

	close(stop)
	wg.Wait()
}
