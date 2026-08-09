package mountsync

import (
	"os"
	"testing"
)

func TestPendingLocalJournalDoesNotClearNewerGeneration(t *testing.T) {
	syncer, err := NewSyncer(&fakeClient{files: map[string]RemoteFile{}}, SyncerOptions{
		WorkspaceID: "ws_pending_generation",
		RemoteRoot:  "/slack/channels/C123/messages",
		LocalRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	const relativePath = "command.json"
	if err := syncer.recordPendingLocalChange(relativePath); err != nil {
		t.Fatalf("record first generation: %v", err)
	}
	first := syncer.pendingLocalChangeToken(relativePath)
	if err := syncer.recordPendingLocalChange(relativePath); err != nil {
		t.Fatalf("record newer generation: %v", err)
	}
	second := syncer.pendingLocalChangeToken(relativePath)
	if string(first) == string(second) {
		t.Fatal("successive observations reused the same journal generation")
	}
	if err := syncer.clearPendingLocalChange(relativePath, first); err != nil {
		t.Fatalf("clear older generation: %v", err)
	}
	if _, err := os.Stat(syncer.pendingLocalChangePath(relativePath)); err != nil {
		t.Fatalf("newer generation was cleared by older upload: %v", err)
	}
	if err := syncer.clearPendingLocalChange(relativePath, second); err != nil {
		t.Fatalf("clear current generation: %v", err)
	}
	if _, err := os.Stat(syncer.pendingLocalChangePath(relativePath)); !os.IsNotExist(err) {
		t.Fatalf("current generation remained after successful clear: %v", err)
	}
}
