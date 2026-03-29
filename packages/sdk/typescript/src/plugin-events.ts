export interface PluginRegisteredEventPayload {
  readonly adapterName: string;
  readonly version: string;
  readonly providerName: string;
  readonly supportedEvents?: readonly string[];
}

export interface PluginIngestedEventPayload<TEvent = unknown, TResult = unknown> {
  readonly adapterName: string;
  readonly workspaceId: string;
  readonly event: TEvent;
  readonly result: TResult;
}

export interface PluginErrorEventPayload<TEvent = unknown> {
  readonly adapterName?: string;
  readonly workspaceId: string;
  readonly event?: TEvent;
  readonly error: Error;
}

export interface PluginEventMap<TEvent = unknown, TResult = unknown> {
  registered: PluginRegisteredEventPayload;
  ingested: PluginIngestedEventPayload<TEvent, TResult>;
  error: PluginErrorEventPayload<TEvent>;
}

export type PluginEventListener<
  TEventMap extends object,
  TEventName extends keyof TEventMap
> = (payload: TEventMap[TEventName]) => void;

export interface PluginEventEmitter<TEventMap extends object = PluginEventMap> {
  emit<TEventName extends keyof TEventMap>(eventName: TEventName, payload: TEventMap[TEventName]): void;
  on<TEventName extends keyof TEventMap>(
    eventName: TEventName,
    listener: PluginEventListener<TEventMap, TEventName>
  ): () => void;
  off<TEventName extends keyof TEventMap>(
    eventName: TEventName,
    listener: PluginEventListener<TEventMap, TEventName>
  ): void;
  clear(eventName?: keyof TEventMap): void;
  listenerCount<TEventName extends keyof TEventMap>(eventName: TEventName): number;
}

type PluginListenerStore<TEventMap extends object> = Partial<{
  [TEventName in keyof TEventMap]: Set<PluginEventListener<TEventMap, TEventName>>;
}>;

export function createPluginEventEmitter<
  TEventMap extends object = PluginEventMap
>(): PluginEventEmitter<TEventMap> {
  const listeners: PluginListenerStore<TEventMap> = {};

  const getListeners = <TEventName extends keyof TEventMap>(
    eventName: TEventName
  ): Set<PluginEventListener<TEventMap, TEventName>> => {
    const existing = listeners[eventName] as Set<PluginEventListener<TEventMap, TEventName>> | undefined;
    if (existing) {
      return existing;
    }

    const created = new Set<PluginEventListener<TEventMap, TEventName>>();
    listeners[eventName] = created as PluginListenerStore<TEventMap>[TEventName];
    return created;
  };

  const readListeners = <TEventName extends keyof TEventMap>(
    eventName: TEventName
  ): Set<PluginEventListener<TEventMap, TEventName>> | undefined => {
    return listeners[eventName] as Set<PluginEventListener<TEventMap, TEventName>> | undefined;
  };

  const emitter: PluginEventEmitter<TEventMap> = {
    emit<TEventName extends keyof TEventMap>(eventName: TEventName, payload: TEventMap[TEventName]): void {
      const registeredListeners = readListeners(eventName);
      if (!registeredListeners || registeredListeners.size === 0) {
        return;
      }

      for (const listener of Array.from(registeredListeners)) {
        listener(payload);
      }
    },

    on<TEventName extends keyof TEventMap>(
      eventName: TEventName,
      listener: PluginEventListener<TEventMap, TEventName>
    ): () => void {
      getListeners(eventName).add(listener);
      return () => {
        emitter.off(eventName, listener);
      };
    },

    off<TEventName extends keyof TEventMap>(
      eventName: TEventName,
      listener: PluginEventListener<TEventMap, TEventName>
    ): void {
      const registeredListeners = readListeners(eventName);
      if (!registeredListeners) {
        return;
      }

      registeredListeners.delete(listener);
      if (registeredListeners.size === 0) {
        delete listeners[eventName];
      }
    },

    clear(eventName?: keyof TEventMap): void {
      if (eventName !== undefined) {
        delete listeners[eventName];
        return;
      }

      for (const key of Object.keys(listeners) as Array<keyof TEventMap>) {
        delete listeners[key];
      }
    },

    listenerCount<TEventName extends keyof TEventMap>(eventName: TEventName): number {
      return readListeners(eventName)?.size ?? 0;
    }
  };

  return emitter;
}
