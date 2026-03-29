import type { RelayFileClient } from "./client.js";
import type { ConnectionProvider } from "./connection.js";
import type { WritebackItem } from "./types.js";

export interface WritebackHandler {
  canHandle(path: string): boolean;
  execute(item: WritebackItem, provider: ConnectionProvider): Promise<void>;
}

export interface WritebackConsumerOptions {
  client: RelayFileClient;
  workspaceId: string;
  handlers: WritebackHandler[];
  provider: ConnectionProvider;
  pollIntervalMs?: number;
  signal?: AbortSignal;
}

const DEFAULT_POLL_INTERVAL_MS = 1_000;

export class WritebackConsumer {
  private readonly client: RelayFileClient;
  private readonly workspaceId: string;
  private readonly handlers: WritebackHandler[];
  private readonly provider: ConnectionProvider;
  private readonly pollIntervalMs: number;
  private readonly signal?: AbortSignal;
  private loopPromise?: Promise<void>;
  private stopped = false;

  constructor(opts: WritebackConsumerOptions) {
    this.client = opts.client;
    this.workspaceId = opts.workspaceId;
    this.handlers = opts.handlers;
    this.provider = opts.provider;
    this.pollIntervalMs = opts.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS;
    this.signal = opts.signal;

    if (this.pollIntervalMs < 0) {
      throw new RangeError("pollIntervalMs must be greater than or equal to 0");
    }
  }

  async start(): Promise<void> {
    if (this.loopPromise) {
      if (this.stopped) {
        // Previous run was stopped — reset and start fresh
        this.stopped = false;
        this.loopPromise = undefined;
      } else {
        return this.loopPromise;
      }
    }
    this.loopPromise = this.runLoop();
    return this.loopPromise;
  }

  stop(): void {
    this.stopped = true;
  }

  async pollOnce(signal = this.signal): Promise<void> {
    const items = await this.client.listPendingWritebacks(this.workspaceId, undefined, signal);

    for (const item of items) {
      if (signal?.aborted || this.stopped) {
        return;
      }

      const handler = this.handlers.find((candidate) => candidate.canHandle(item.path));

      if (!handler) {
        await this.ackFailure(item, new Error(`No writeback handler found for path: ${item.path}`), signal);
        continue;
      }

      let executed = false;
      try {
        await handler.execute(item, this.provider);
        executed = true;
      } catch (error) {
        if (isAbortError(error) || signal?.aborted) {
          throw error;
        }
        await this.ackFailure(item, error, signal);
      }

      if (executed) {
        try {
          await this.client.ackWriteback({
            workspaceId: this.workspaceId,
            itemId: item.id,
            success: true,
            correlationId: item.correlationId,
            signal
          });
        } catch (ackError) {
          if (isAbortError(ackError) || signal?.aborted) {
            throw ackError;
          }
          // Handler succeeded — do NOT send failure ack (would cause duplicate writes on retry).
          // Log and move on; server will eventually time out and retry the ack.
        }
      }
    }
  }

  private async runLoop(): Promise<void> {
    while (!this.stopped && !this.signal?.aborted) {
      try {
        await this.pollOnce(this.signal);

        if (this.stopped || this.signal?.aborted) {
          return;
        }

        await sleep(this.pollIntervalMs, this.signal);
      } catch (error) {
        if (isAbortError(error) || this.signal?.aborted || this.stopped) {
          return;
        }
        throw error;
      }
    }
  }

  private async ackFailure(item: WritebackItem, error: unknown, signal?: AbortSignal): Promise<void> {
    await this.client.ackWriteback({
      workspaceId: this.workspaceId,
      itemId: item.id,
      success: false,
      error: toErrorMessage(error),
      correlationId: item.correlationId,
      signal
    });
  }
}

async function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  if (ms === 0) {
    return;
  }

  if (signal?.aborted) {
    throw signal.reason ?? new DOMException("The operation was aborted", "AbortError");
  }

  await new Promise<void>((resolve, reject) => {
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal!.reason ?? new DOMException("The operation was aborted", "AbortError"));
    };

    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);

    if (!signal) {
      return;
    }

    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function toErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }

  return String(error);
}
