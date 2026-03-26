package mountfuse

import (
	"context"
	"syscall"
	"time"

	gofusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type DirNode struct {
	gofusefs.Inode
	state *fsState
	path  string
}

var _ gofusefs.InodeEmbedder = (*DirNode)(nil)
var _ gofusefs.NodeGetattrer = (*DirNode)(nil)
var _ gofusefs.NodeLookuper = (*DirNode)(nil)
var _ gofusefs.NodeReaddirer = (*DirNode)(nil)
var _ gofusefs.NodeMkdirer = (*DirNode)(nil)
var _ gofusefs.NodeCreater = (*DirNode)(nil)
var _ gofusefs.NodeUnlinker = (*DirNode)(nil)

func newDirNode(state *fsState, remotePath string) *DirNode {
	return &DirNode{
		state: state,
		path:  normalizeRemotePath(remotePath),
	}
}

// State returns the shared filesystem state for use by WSInvalidator.
func (n *DirNode) State() *fsState {
	return n.state
}

func (n *DirNode) Getattr(ctx context.Context, _ gofusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	meta, err := n.state.lookupMetadata(ctx, n.path)
	if err != nil && n.path == n.state.remoteRoot {
		meta = nodeMeta{
			path:    n.path,
			mode:    syscall.S_IFDIR | defaultDirMode,
			modTime: time.Now(),
		}
		err = nil
	}
	if err != nil {
		return readErrno(err)
	}
	meta.fillAttr(out, n.state)
	return 0
}

func (n *DirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofusefs.Inode, syscall.Errno) {
	if child := n.GetChild(name); child != nil {
		if existing, ok := child.Operations().(interface {
			fillEntry(*fuse.EntryOut) syscall.Errno
		}); ok {
			if errno := existing.fillEntry(out); errno != 0 {
				return nil, errno
			}
		}
		return child, 0
	}
	entries, err := n.state.listDirectory(ctx, n.path)
	if err != nil {
		return nil, readErrno(err)
	}
	meta, ok := entries[name]
	if !ok {
		out.SetEntryTimeout(n.state.negativeTTL)
		return nil, syscall.ENOENT
	}
	if meta.isFile() && meta.size == 0 {
		if file, err := n.state.readFile(ctx, meta.path); err == nil {
			meta.size = uint64(len(file.Content))
			meta.revision = file.Revision
			meta.contentType = file.ContentType
		}
	}
	meta.fillEntry(out, n.state)
	return newChildNode(n, meta), 0
}

func (n *DirNode) Readdir(ctx context.Context) (gofusefs.DirStream, syscall.Errno) {
	entries, err := n.state.listDirectory(ctx, n.path)
	if err != nil {
		return nil, readErrno(err)
	}
	return gofusefs.NewListDirStream(sortedDirEntries(entries, n.state)), 0
}

func (n *DirNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*gofusefs.Inode, syscall.Errno) {
	_ = ctx
	_ = name
	_ = mode
	_ = out
	// The current HTTP API only exposes file write/delete operations, so there
	// is no durable directory primitive to back mkdir yet.
	return nil, syscall.ENOTSUP
}

func (n *DirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*gofusefs.Inode, gofusefs.FileHandle, uint32, syscall.Errno) {
	remotePath := joinRemotePath(n.path, name)
	meta := nodeMeta{
		path:        remotePath,
		name:        name,
		mode:        syscall.S_IFREG | (mode & 0o777),
		revision:    "0",
		contentType: contentTypeForPath(remotePath),
		modTime:     time.Now(),
	}
	if meta.mode&0o777 == 0 {
		meta.mode = syscall.S_IFREG | defaultFileMode
	}
	node := newFileNode(n.state, meta)
	child := n.NewInode(ctx, node, gofusefs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  n.state.inodeFor(remotePath, false),
	})
	out.Attr.Mode = meta.mode
	out.Attr.Size = 0
	out.Attr.Ino = n.state.inodeFor(remotePath, false)
	out.Attr.Owner = fuse.Owner{Uid: n.state.uid, Gid: n.state.gid}
	out.SetEntryTimeout(n.state.entryTTL)
	out.SetAttrTimeout(n.state.attrTTL)
	handle := node.newHandle([]byte{}, meta.revision, meta.contentType, true)
	n.state.invalidate(remotePath)
	return child, handle, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *DirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	entries, err := n.state.listDirectory(ctx, n.path)
	if err != nil {
		return writeErrno(err)
	}
	meta, ok := entries[name]
	if !ok {
		return syscall.ENOENT
	}
	if meta.isDir() {
		return syscall.EISDIR
	}
	baseRevision := meta.revision
	if baseRevision == "" {
		file, err := n.state.readFile(ctx, meta.path)
		if err != nil {
			return writeErrno(err)
		}
		baseRevision = file.Revision
	}
	if err := n.state.client.DeleteFile(ctx, n.state.workspaceID, meta.path, baseRevision); err != nil {
		return writeErrno(err)
	}
	n.state.invalidate(meta.path)
	n.RmChild(name)
	return 0
}

func (n *DirNode) fillEntry(out *fuse.EntryOut) syscall.Errno {
	meta := nodeMeta{
		path:    n.path,
		mode:    syscall.S_IFDIR | defaultDirMode,
		modTime: time.Now(),
	}
	meta.fillEntry(out, n.state)
	return 0
}
