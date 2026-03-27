package mountfuse

import (
	"context"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
	gofusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// maxFileBytes is the maximum file size allowed, matching the core package limit.
const maxFileBytes = 10 * 1024 * 1024 // 10 MiB

type FileNode struct {
	gofusefs.Inode
	state *fsState
	path  string

	mu          sync.RWMutex
	revision    string
	size        uint64
	modTime     time.Time
	contentType string
}

type FileHandle struct {
	node *FileNode

	mu          sync.Mutex
	buf         []byte
	revision    string
	contentType string
	dirty       bool
	writeGen    uint64 // incremented on each Write; used to detect concurrent writes during flush
}

var _ gofusefs.InodeEmbedder = (*FileNode)(nil)
var _ gofusefs.NodeGetattrer = (*FileNode)(nil)
var _ gofusefs.NodeOpener = (*FileNode)(nil)
var _ gofusefs.NodeFsyncer = (*FileNode)(nil)
var _ gofusefs.FileReader = (*FileHandle)(nil)
var _ gofusefs.FileWriter = (*FileHandle)(nil)
var _ gofusefs.FileFlusher = (*FileHandle)(nil)
var _ gofusefs.FileFsyncer = (*FileHandle)(nil)
var _ gofusefs.FileReleaser = (*FileHandle)(nil)

func newFileNode(state *fsState, meta nodeMeta) *FileNode {
	return &FileNode{
		state:       state,
		path:        normalizeRemotePath(meta.path),
		revision:    meta.revision,
		size:        meta.size,
		modTime:     zeroTimeToNow(meta.modTime),
		contentType: meta.contentType,
	}
}

func (n *FileNode) Getattr(ctx context.Context, _ gofusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	meta, err := n.metadata(ctx)
	if err != nil {
		return readErrno(err)
	}
	meta.fillAttr(out, n.state)
	return 0
}

func (n *FileNode) Open(ctx context.Context, flags uint32) (gofusefs.FileHandle, uint32, syscall.Errno) {
	file, err := n.state.readFile(ctx, n.path)
	if err != nil {
		return nil, 0, readErrno(err)
	}
	n.updateFromRemote(file)
	handle := n.newHandle([]byte(file.Content), file.Revision, file.ContentType, flags&syscall.O_TRUNC != 0)
	if flags&syscall.O_TRUNC != 0 {
		handle.buf = handle.buf[:0]
	}
	return handle, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *FileNode) Fsync(ctx context.Context, f gofusefs.FileHandle, flags uint32) syscall.Errno {
	if handle, ok := f.(*FileHandle); ok {
		return handle.Fsync(ctx, flags)
	}
	return 0
}

func (n *FileNode) metadata(ctx context.Context) (nodeMeta, error) {
	n.mu.RLock()
	revision := n.revision
	size := n.size
	modTime := n.modTime
	contentType := n.contentType
	n.mu.RUnlock()
	if revision == "" || size == 0 {
		file, err := n.state.readFile(ctx, n.path)
		if err != nil {
			return nodeMeta{}, err
		}
		n.updateFromRemote(file)
		revision = file.Revision
		size = uint64(len(file.Content))
		modTime = time.Now()
		contentType = file.ContentType
	}
	return nodeMeta{
		path:        n.path,
		name:        pathBase(n.path),
		mode:        syscall.S_IFREG | defaultFileMode,
		revision:    revision,
		size:        size,
		modTime:     zeroTimeToNow(modTime),
		contentType: contentType,
	}, nil
}

func (n *FileNode) updateFromRemote(file mountsync.RemoteFile) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.revision = file.Revision
	n.size = uint64(len(file.Content))
	n.modTime = time.Now()
	n.contentType = file.ContentType
	if n.contentType == "" {
		n.contentType = contentTypeForPath(n.path)
	}
}

func (n *FileNode) updateFromBuffer(buf []byte, revision, contentType string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.revision = revision
	n.size = uint64(len(buf))
	n.modTime = time.Now()
	if contentType != "" {
		n.contentType = contentType
	}
}

func (n *FileNode) newHandle(buf []byte, revision, contentType string, dirty bool) *FileHandle {
	if contentType == "" {
		contentType = contentTypeForPath(n.path)
	}
	return &FileHandle{
		node:        n,
		buf:         append([]byte(nil), buf...),
		revision:    revision,
		contentType: contentType,
		dirty:       dirty,
	}
}

func (h *FileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	_ = ctx
	h.mu.Lock()
	defer h.mu.Unlock()
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if off >= int64(len(h.buf)) {
		return fuse.ReadResultData(nil), 0
	}
	end := int(off) + len(dest)
	if end > len(h.buf) {
		end = len(h.buf)
	}
	data := append([]byte(nil), h.buf[off:end]...)
	return fuse.ReadResultData(data), 0
}

func (h *FileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	_ = ctx
	h.mu.Lock()
	defer h.mu.Unlock()
	if off < 0 {
		return 0, syscall.EINVAL
	}
	end := int(off) + len(data)
	if end > maxFileBytes {
		return 0, syscall.EFBIG
	}
	if end > len(h.buf) {
		grown := make([]byte, end)
		copy(grown, h.buf)
		h.buf = grown
	}
	copy(h.buf[int(off):end], data)
	h.dirty = true
	h.writeGen++
	return uint32(len(data)), 0
}

func (h *FileHandle) Flush(ctx context.Context) syscall.Errno {
	return h.flush(ctx)
}

func (h *FileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	_ = flags
	return h.flush(ctx)
}

func (h *FileHandle) Release(ctx context.Context) syscall.Errno {
	return h.flush(ctx)
}

func (h *FileHandle) flush(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	if !h.dirty {
		h.mu.Unlock()
		return 0
	}
	body := append([]byte(nil), h.buf...)
	baseRevision := h.revision
	contentType := h.contentType
	genSnapshot := h.writeGen
	h.mu.Unlock()

	result, err := h.node.state.client.WriteFile(ctx, h.node.state.workspaceID, h.node.path, baseRevision, contentType, string(body))
	if err != nil {
		return writeErrno(err)
	}

	file := mountsync.RemoteFile{
		Path:        h.node.path,
		Revision:    result.TargetRevision,
		ContentType: contentType,
		Content:     string(body),
	}
	h.node.state.invalidate(h.node.path)
	h.node.state.putFile(file)
	h.node.updateFromBuffer(body, result.TargetRevision, contentType)

	h.mu.Lock()
	h.revision = result.TargetRevision
	if h.writeGen == genSnapshot {
		// No concurrent Write() happened; safe to reset buffer and clear dirty flag.
		h.buf = append(h.buf[:0], body...)
		h.dirty = false
	}
	// If writeGen changed, a concurrent Write() modified buf — keep the new
	// data and dirty flag so the next flush persists it.
	h.mu.Unlock()
	return 0
}

func (n *FileNode) fillEntry(out *fuse.EntryOut) syscall.Errno {
	n.mu.RLock()
	meta := nodeMeta{
		path:        n.path,
		mode:        syscall.S_IFREG | defaultFileMode,
		revision:    n.revision,
		size:        n.size,
		modTime:     zeroTimeToNow(n.modTime),
		contentType: n.contentType,
	}
	n.mu.RUnlock()
	meta.fillEntry(out, n.state)
	return 0
}

func zeroTimeToNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now()
	}
	return value
}

func pathBase(remotePath string) string {
	if remotePath == "/" {
		return "/"
	}
	return remotePath[strings.LastIndex(remotePath, "/")+1:]
}
