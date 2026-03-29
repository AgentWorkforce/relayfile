export {
  RelayFileClient,
  DEFAULT_RELAYFILE_BASE_URL,
  type ConnectWebSocketOptions,
  type RouteWebhookInput,
  type RelayFileRetryOptions,
  type WebSocketConnection
} from "./client.js";
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
  InvalidPluginContractError,
  PayloadTooLargeError,
  PluginValidationError,
  QueueFullError,
  RelayFileApiError,
  RevisionConflictError,
  DuplicateAdapterError,
  DuplicateAdapterNameError,
  MissingAdapterError
} from "./errors.js";
export type {
  InvalidPluginContractErrorOptions,
  PluginContractType
} from "./errors.js";
export {
  IntegrationAdapter,
  type IngestError,
  type IngestResult,
  type SyncOptions,
  type SyncResult
} from "./adapter.js";

// Config schema helpers
export {
  ConfigSchemaDefinitionError,
  defineAdapterConfigSchema,
  defineProviderConfigSchema,
  isJsonObject,
  matchesJsonSchemaValue,
  validateAdapterConfigSchema,
  validateJsonSchemaValue,
  validateProviderConfigSchema
} from "./config-schema.js";
export type {
  AdapterConfigSchema,
  InferJsonSchema,
  JsonObject,
  JsonPrimitive,
  JsonSchema,
  JsonSchemaArray,
  JsonSchemaBoolean,
  JsonSchemaInteger,
  JsonSchemaMetadata,
  JsonSchemaNull,
  JsonSchemaNumber,
  JsonSchemaObject,
  JsonSchemaProperties,
  JsonSchemaString,
  JsonSchemaTypeName,
  JsonSchemaVersionMetadata,
  JsonValue,
  PluginConfigSchemaBase,
  PluginConfigVersionMetadata,
  PluginKind,
  ProviderConfigSchema
} from "./config-schema.js";

// Plugin registry
export {
  AdapterRegistry,
  bootstrapRelayFileClientAdapters,
  registerDiscoveredAdapters,
  type AdapterRegistryEventMap
} from "./registry.js";
export type {
  BootstrapRelayFileClientAdaptersOptions,
  BootstrapRelayFileClientAdaptersResult,
  RegisterDiscoveredAdaptersOptions,
  RegisterDiscoveredAdaptersResult
} from "./registry.js";

// Plugin loading
export {
  ADAPTER_PACKAGE_PREFIX,
  discoverInstalledAdapters,
  loadAdapterModule
} from "./plugin-loader.js";
export type {
  AdapterClass,
  AdapterFactory,
  AdapterLoaderDiagnostic,
  AdapterLoaderDiagnosticCode,
  AdapterLoaderDiagnosticSeverity,
  AdapterModuleExport,
  AdapterModuleExportKind,
  AdapterModuleNamespace,
  DiscoverInstalledAdaptersResult,
  InstalledAdapterDescriptor,
  LoadedAdapterMetadata,
  LoadAdapterModuleFailure,
  LoadAdapterModuleResult,
  LoadAdapterModuleSuccess
} from "./plugin-loader.js";

// Plugin bridge helpers
export {
  instantiateLoadedAdapter,
  loadAndRegisterAdapter,
  registerLoadedAdapter
} from "./plugin-bridge.js";
export type {
  AdapterRegistrar,
  InstantiateLoadedAdapterResult,
  InstantiateLoadedAdapterSuccess,
  LoadAndRegisterAdapterResult,
  PluginBridgeDiagnosticCode,
  PluginBridgeFailure,
  PluginBridgeStage,
  RegisterLoadedAdapterResult,
  RegisterLoadedAdapterSuccess
} from "./plugin-bridge.js";

// Plugin validation
export {
  assertRequiredFunction,
  assertSemverishVersion,
  assertStringName,
  validateConnectionProvider,
  validateIntegrationAdapter,
  validateRegistrationInput
} from "./validation.js";
export type {
  ConnectionProviderLike,
  IntegrationAdapterLike,
  RegistrationFactory,
  ValidatedConnectionProvider,
  ValidatedIntegrationAdapter,
  ValidatedRegistrationAdapter,
  ValidatedRegistrationFactory,
  ValidatedRegistrationInput
} from "./validation.js";

// Plugin events
export { createPluginEventEmitter } from "./plugin-events.js";
export type {
  PluginErrorEventPayload,
  PluginEventEmitter,
  PluginEventListener,
  PluginEventMap,
  PluginIngestedEventPayload,
  PluginRegisteredEventPayload
} from "./plugin-events.js";
// Integration providers
export {
  IntegrationProvider,
  WebhookNormalizationError,
  computeCanonicalPath,
  fromWebhookInput,
  isEventType,
  isNormalizedWebhook,
  isObjectId,
  isObjectType,
  isProvider,
  normalizeWebhook,
  toIngestWebhookInput
} from "./provider.js";
export type {
  ConnectionProvider,
  IngestWebhookRequest,
  ListProviderFilesOptions,
  NormalizedWebhook,
  ProviderMetadata,
  ProviderPayload,
  ProxyHeaders,
  ProxyMethod,
  ProxyQuery,
  ProxyRequest,
  ProxyResponse,
  WebhookEventType,
  WebhookNormalizationErrorCode,
  WebhookNormalizationField,
  WebhookObjectId,
  WebhookObjectType,
  WebhookProvider,
  WatchProviderEventsOptions,
  WebhookInput
} from "./provider.js";
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
  ConflictErrorResponse,
  DeleteFileInput,
  DeadLetterFeedResponse,
  DeadLetterItem,
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

// Testing helpers stay behind a dedicated testing namespace.
export * as testing from "./testing/index.js";
