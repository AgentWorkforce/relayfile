export {
  RelayFileClient,
  DEFAULT_RELAYFILE_BASE_URL,
  type AccessTokenProvider,
  type RelayFileChangeLogOptions,
  type ConnectWebSocketOptions,
  type RelayFileClientOptions,
  type RelayFileRetryOptions,
  type WebSocketConnection
} from "./client.js";
export type { RelayFileReadCacheOptions } from "./types.js";
export {
  RelayfileSetup,
  RELAYFILE_SDK_VERSION,
  WorkspaceHandle
} from "./setup.js";
export {
  type RelayfileCloudLoginOptions,
  type RelayfileCloudTokenSet,
  type RelayfileCloudTokenSetupOptions
} from "./cloud-login.js";
export {
  CloudAbortError,
  CloudApiError,
  CloudTimeoutError,
  InvalidLocalDirError,
  InvalidMountModeError,
  InvalidRemotePathError,
  IntegrationConnectionTimeoutError,
  MalformedCloudResponseError,
  MissingConnectionIdError,
  MountModeUnavailableError,
  MountReadyTimeoutError,
  MountSessionInputError,
  ProviderNotConnectedError,
  ProviderNotReadyError,
  RelayfileSetupError,
  UnknownProviderError
} from "./setup-errors.js";
export {
  type EnsureMountedWorkspaceInput,
  WORKSPACE_INTEGRATION_PROVIDERS,
  type AgentWorkspaceInvite,
  type AgentWorkspaceInviteOptions,
  type AgentWorkspaceScopedInviteOptions,
  type ConnectIntegrationOptions,
  type ConnectIntegrationResult,
  type CreateWorkspaceOptions,
  type JoinWorkspaceOptions,
  type MountLauncher,
  type MountLauncherEvent,
  type MountLauncherInstance,
  type MountLauncherStart,
  type MountLocalLayout,
  type MountMode,
  type MountSessionRequest,
  type MountSessionResponse,
  type MountSessionResult,
  type MountSyncMode,
  type MountedWorkspaceHandle,
  type MountedWorkspaceStatus,
  type MountWorkspaceInput,
  type ReadMountedWorkspaceStatusInput,
  type RelayfileSetupOptions,
  type RelayfileSetupRetryOptions,
  type WaitForConnectionOptions,
  type WorkspaceInfo,
  type WorkspaceIntegrationProvider,
  type WorkspaceMountEnv,
  type WorkspaceMountEnvOptions,
  type WorkspacePermissions
} from "./setup-types.js";
export {
  RelayFileSync,
  type RelayFileSyncOptions,
  type RelayFileSyncPong,
  type RelayFileSyncReconnectOptions,
  type RelayFileSyncSocket,
  type RelayFileSyncStart,
  type RelayFileSyncState,
  type RelayFileSyncTokenProvider
} from "./sync.js";
export {
  onWrite,
  pathMatches,
  type OnWriteClient,
  type OnWriteHandler,
  type OnWriteHandlerError,
  type OnWriteOptions
} from "./onWrite.js";
export {
  InvalidStateError,
  PayloadTooLargeError,
  QueueFullError,
  RelayFileApiError,
  RevisionConflictError
} from "./errors.js";
// Integration providers
export { IntegrationProvider, computeCanonicalPath } from "./provider.js";
export type { WebhookInput, ListProviderFilesOptions, WatchProviderEventsOptions } from "./provider.js";
// Connection provider contract
export type {
  ConnectCapableProvider,
  ConnectConnectionStatus,
  ConnectSession,
  ConnectionProvider,
  CreateConnectSessionInput,
  GetConnectConnectionStatusInput,
  NormalizedWebhook,
  ProviderConfigKeyMap,
  ProxyHeaders,
  ProxyMethod,
  ProxyQuery,
  ProxyRequest,
  ProxyResponse,
} from "./connection.js";
export { supportsConnect } from "./connection.js";
export { SelfHostConnect } from "./self-host-connect.js";
export type {
  SelfHostConnectOptions,
  SelfHostConnectResult,
  StartSelfHostConnectOptions,
  WaitForSelfHostConnectionOptions
} from "./self-host-connect.js";

export type {
  AckResponse,
  AckWritebackInput,
  AckWritebackDraftDisposition,
  AckWritebackResponse,
  AdminIngressAlert,
  AdminIngressAlertProfile,
  AdminIngressEffectiveAlertProfile,
  AdminIngressAlertSeverity,
  AdminIngressAlertThresholds,
  AdminIngressAlertTotals,
  AdminIngressAlertType,
  AdminIngressStatusResponse,
  AdminSyncAlert,
  AdminSyncAlertSeverity,
  AdminSyncAlertThresholds,
  AdminSyncAlertTotals,
  AdminSyncAlertType,
  AdminSyncStatusResponse,
  BackendStatusResponse,
  BulkWriteFile,
  BulkWriteInput,
  BulkWriteResponse,
  ChangeLogQueryResult,
  ChangeEvent,
  ChangeEventActor,
  ChangeEventResource,
  ChangeEventSummary,
  ChangeStreamConnection,
  ChangeStreamConnectionOptions,
  CommitForkInput,
  CommitForkResponse,
  ConflictErrorResponse,
  CreateForkInput,
  ContentIdentity,
  DeleteFileInput,
  DeleteWebhookOptions,
  DeadLetterFeedResponse,
  DeadLetterItem,
  DigestBullet,
  DigestContext,
  DigestHandler,
  DigestSection,
  DigestWindow,
  DiscardForkInput,
  ErrorResponse,
  EventSummary,
  EventFeedResponse,
  ExportFormat,
  ExportJsonResponse,
  ExportOptions,
  FileQueryItem,
  FileQueryResponse,
  FileReadResponse,
  FileSemantics,
  FileWriteRequest,
  FilesystemEvent,
  FilesystemEventType,
  EventOrigin,
  Expansion,
  ExpansionLevel,
  GetEventsOptions,
  GetAdminSyncStatusOptions,
  GetAdminIngressStatusOptions,
  GetOperationsOptions,
  GetSyncDeadLettersOptions,
  GetSyncIngressStatusOptions,
  GetSyncStatusOptions,
  WaitForDataOptions,
  GetWebhookDeadLettersOptions,
  IngestWebhookInput,
  LayoutManifest,
  LayoutManifestAlias,
  LayoutManifestResource,
  ListWebhooksOptions,
  ListTreeOptions,
  OperationFeedResponse,
  OperationStatus,
  OperationStatusResponse,
  QueuedResponse,
  QueryFilesOptions,
  ReadFileInput,
  RegisterWebhookInput,
  RegisterWebhookResponse,
  ReplayOptions,
  ResourceAtEventResult,
  SummaryExpansion,
  FullExpansion,
  DiffExpansion,
  ThreadExpansion,
  RelayFileJwtClaims,
  SubscribeOptions,
  Subscription,
  SyncIngressStatusResponse,
  SyncProviderStatus,
  SyncProviderStatusState,
  SyncRefreshRequest,
  SyncStatusResponse,
  SweepWritebackDraftsInput,
  SweepWritebackDraftsResponse,
  TreeEntry,
  TreeResponse,
  WebhookDeliveryDeadLetterFeedResponse,
  WebhookDeliveryDeadLetterItem,
  WebhookSubscription,
  WebhookSubscriptionHealth,
  WritebackActionType,
  WritebackDeadLetterError,
  WritebackDeadLetterErrorCode,
  WritebackListState,
  WritebackState,
  WritebackItem,
  WritebackItemDetail,
  WritebackSchemaRef,
  WriteFileInput,
  WriteQueuedResponse
} from "./types.js";
export type { ForkHandle, ForkOptions } from "@relayfile/core";
export type { WriteEvent, WriteEventActor, WriteEventOperation, WriteEventSource } from "@relayfile/core";

export { WritebackConsumer } from "./writeback-consumer.js";
export type { WritebackHandler, WritebackConsumerOptions } from "./writeback-consumer.js";
export * from "./integration-adapter.js";

// Agent workspace provisioning helpers (`seedWorkspace`, `seedAclRules`,
// `ensureRelayfileMount`, …) live in CLI-only modules that statically pull in
// `node:child_process`, `node:fs`, and `node:path`. They are intentionally
// excluded from the default entry so browser/edge consumers stay node-free.
// Import them from the explicit subpaths instead:
//   `@relayfile/sdk/workspace-seeder`
//   `@relayfile/sdk/workspace-mount`
