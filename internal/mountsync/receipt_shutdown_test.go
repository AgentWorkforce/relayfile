package mountsync

import (
	"context"
	"errors"
	"testing"
	"time"
)

type receiptResultClient struct {
	*fakeClient
	getOperation func(context.Context) (OperationStatus, error)
}

func (c *receiptResultClient) GetOperation(ctx context.Context, _, _ string) (OperationStatus, error) {
	return c.getOperation(ctx)
}

func TestReceiptSettlementShutdownPreservesPendingRecord(t *testing.T) {
	transportError := errors.New("connection reset by peer")
	for _, test := range []struct {
		name     string
		shutdown bool
		pollErr  error
	}{
		{name: "shutdown cancellation", shutdown: true, pollErr: context.Canceled},
		{name: "shutdown transport failure", shutdown: true, pollErr: transportError},
		{name: "live transport failure", pollErr: transportError},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			client := &receiptResultClient{
				fakeClient: &fakeClient{files: map[string]RemoteFile{}},
				getOperation: func(ctx context.Context) (OperationStatus, error) {
					close(started)
					if test.shutdown {
						<-ctx.Done()
					}
					return OperationStatus{}, test.pollErr
				},
			}
			syncer, err := NewSyncer(client, SyncerOptions{
				WorkspaceID: "ws_receipt_shutdown",
				RemoteRoot:  "/",
				LocalRoot:   t.TempDir(),
				WebSocket:   boolPtr(false),
			})
			if err != nil {
				t.Fatalf("new syncer: %v", err)
			}
			t.Cleanup(syncer.Close)
			record := outboxRecord{
				CommandID:      "receipt_command",
				WorkspaceID:    syncer.workspace,
				RemotePath:     "/note.txt",
				Status:         outboxStatusPending,
				OpID:           "receipt_operation",
				DispatchStatus: "pending",
				AttemptCount:   1,
			}
			if err := syncer.saveOutboxRecord(record); err != nil {
				t.Fatalf("save pending receipt: %v", err)
			}
			record, err = syncer.readOutboxRecord(syncer.pendingOutboxPath(record.CommandID))
			if err != nil {
				t.Fatalf("read initial receipt: %v", err)
			}
			beforeCircuit := syncer.Circuit().Snapshot()
			syncer.scheduleOutboxReceiptSettlements([]outboxRecord{record})
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("receipt poll did not start")
			}
			settled := make(chan struct{})
			go func() {
				if test.shutdown {
					syncer.Close()
				} else {
					syncer.backgroundWG.Wait()
				}
				close(settled)
			}()
			select {
			case <-settled:
			case <-time.After(time.Second):
				t.Fatal("receipt worker did not terminate")
			}
			after, err := syncer.readOutboxRecord(syncer.pendingOutboxPath(record.CommandID))
			if err != nil {
				t.Fatalf("read pending receipt: %v", err)
			}
			afterCircuit := syncer.Circuit().Snapshot()
			if test.shutdown {
				if after != record {
					t.Errorf("shutdown changed pending receipt: got %+v, want %+v", after, record)
				}
				if afterCircuit != beforeCircuit {
					t.Errorf("shutdown changed circuit: got %+v, want %+v", afterCircuit, beforeCircuit)
				}
			} else {
				if after.AttemptCount != record.AttemptCount+1 || after.LastError == "" || after.NextAttemptAt == "" {
					t.Errorf("live failure did not schedule retry: %+v", after)
				}
				if afterCircuit.Failures != beforeCircuit.Failures+1 {
					t.Errorf("live failure count = %d, want %d", afterCircuit.Failures, beforeCircuit.Failures+1)
				}
			}
		})
	}
}
