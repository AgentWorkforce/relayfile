export {
  RelayFileClient,
  DEFAULT_RELAYFILE_BASE_URL,
  type AccessTokenProvider,
  type ConnectWebSocketOptions,
  type RelayFileClientOptions,
  type RelayFileRetryOptions,
  type WebSocketConnection
} from "./client.js";
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
  IntegrationConnectionTimeoutError,
  MalformedCloudResponseError,
  MissingConnectionIdError,
  RelayfileSetupError,
  UnknownProviderError
} from "./setup-errors.js";
export {
  WORKSPACE_INTEGRATION_PROVIDERS,
  type AgentWorkspaceInvite,
  type AgentWorkspaceInviteOptions,
  type ConnectIntegrationOptions,
  type ConnectIntegrationResult,
  type CreateWorkspaceOptions,
  type JoinWorkspaceOptions,
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
  type RelayFileSyncState
} from "./sync.js";
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
  ConnectionProvider,
  NormalizedWebhook,
  ProxyHeaders,
  ProxyMethod,
  ProxyQuery,
  ProxyRequest,
  ProxyResponse,
} from "./connection.js";

export type {
  AckResponse,
  AckWritebackInput,
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
  CommitForkInput,
  CommitForkResponse,
  ConflictErrorResponse,
  CreateForkInput,
  ContentIdentity,
  DeleteFileInput,
  DeadLetterFeedResponse,
  DeadLetterItem,
  DiscardForkInput,
  ErrorResponse,
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
  GetEventsOptions,
  GetAdminSyncStatusOptions,
  GetAdminIngressStatusOptions,
  GetOperationsOptions,
  GetSyncDeadLettersOptions,
  GetSyncIngressStatusOptions,
  GetSyncStatusOptions,
  IngestWebhookInput,
  ListTreeOptions,
  OperationFeedResponse,
  OperationStatus,
  OperationStatusResponse,
  QueuedResponse,
  QueryFilesOptions,
  ReadFileInput,
  RelayFileJwtClaims,
  SyncIngressStatusResponse,
  SyncProviderStatus,
  SyncProviderStatusState,
  SyncRefreshRequest,
  SyncStatusResponse,
  TreeEntry,
  TreeResponse,
  WritebackActionType,
  WritebackState,
  WritebackItem,
  WriteFileInput,
  WriteQueuedResponse
} from "./types.js";
export type { ForkHandle, ForkOptions } from "@relayfile/core";

export { WritebackConsumer } from "./writeback-consumer.js";
export type { WritebackHandler, WritebackConsumerOptions } from "./writeback-consumer.js";
export * from "./integration-adapter.js";
