import { Actor, Ephemeral, type ActorContext, type ActorSocket } from "durable-actors"
import { WorkspaceRuntime, type SocketPort } from "../../src/runtime/workspace-runtime.js"
import type { ActorHttpRequest, ActorHttpResponse, CallerContext, SocketMetadata } from "../../src/runtime/types.js"

type Outgoing = Record<string, unknown>
type Incoming = Record<string, unknown>
type Socket = ActorSocket<SocketMetadata, Outgoing>

/**
 * One relayfile workspace. The actor id is the workspace id; the workspace's
 * files, events, writeback operations and webhook envelopes live in this
 * actor's SQLite. File bytes above the gateway's inline threshold live in
 * object storage and only their sha256 refs are stored here.
 *
 * The gateway verifies the caller's bearer token, derives the actor id from
 * the verified workspace claim, and calls `handle`. Each call is one SQLite
 * transaction that rolls back if it throws (durable-actors 0.14.6 statements
 * autocommit, so the runtime wraps every entry point in `db.transaction`).
 */
export class Workspace extends Actor<SocketMetadata, Incoming, Outgoing> {
    @Ephemeral private runtime: WorkspaceRuntime

    constructor(context: ActorContext) {
        super(context)
        const sockets: SocketPort = {
            broadcast: (message, tags) => this.broadcast(message, tags ? { tags } : undefined),
            connections: async () =>
                (await this.getConnections()).map(socket => ({
                    id: socket.id,
                    metadata: socket.metadata,
                    send: (message: Outgoing) => socket.send(message),
                })),
        }
        this.runtime = new WorkspaceRuntime(this.db, this.id, { alarm: this.alarm, sockets })
    }

    /** Serve one relayfile API request for this workspace. */
    async handle(request: ActorHttpRequest, caller: CallerContext): Promise<ActorHttpResponse> {
        return this.db.transaction(() => this.runtime.handle(request, caller))
    }

    /** Writeback retry scheduling and event retention. */
    async onAlarm(): Promise<void> {
        await this.db.transaction(() => this.runtime.maintain())
    }

    async onConnect(socket: Socket): Promise<void> {
        await this.db.transaction(() =>
            this.runtime.connect({ id: socket.id, metadata: socket.metadata, send: message => socket.send(message) }, tags =>
                socket.setTags(...tags)
            )
        )
    }

    async onMessage(socket: Socket, message: Incoming): Promise<void> {
        this.runtime.message({ id: socket.id, metadata: socket.metadata, send: m => socket.send(m) }, message)
    }
}
