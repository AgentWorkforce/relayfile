# Storage Bridge Spec

Real-time change notifications from infrastructure storage backends into Relayfile workspaces.

> **Status (2026-05-09):** Specification. No implementation yet. Companion to `nango-bridge-design.md`, which covers SaaS providers (Zendesk, Shopify, Linear, etc.). This document covers infrastructure storage — S3, Postgres, Redis, GCS, Azure Blob — which cannot be integrated via Nango's OAuth+webhook model.

---

## Problem

Relayfile's event model assumes providers push changes via HTTP webhooks. This works well for SaaS tools that have webhook infrastructure. Infrastructure storage systems do not:

| System | Native push mechanism | Usable for relayfile? |
|---|---|---|
| AWS S3 | S3 Event Notifications → SNS/SQS | Yes, with a bridge |
| Postgres | `LISTEN/NOTIFY`, logical replication | Yes, with a bridge |
| Redis | Keyspace notifications (internal pub/sub) | Yes, with a bridge |
| GCS | Pub/Sub notifications | Yes, with a bridge |
| Azure Blob | Event Grid | Yes, with a bridge |
| SSH/SFTP | Nothing | Poll only |
| Local filesystem | inotify/FSEvents | Yes, with a bridge |

Without bridges, agents accessing these systems must either poll (slow, wasteful) or use mirage's pull-on-demand cache (up to 10 min stale). The storage bridge closes this gap, making relayfile's push-driven consistency model available across the full source universe.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Storage Systems                                 │
│  S3  │  Postgres  │  Redis  │  GCS  │  Azure Blob  │  SFTP         │
└──┬───┴─────┬──────┴────┬────┴───┬───┴──────┬───────┴──┬────────────┘
   │         │           │        │           │          │
   │ S3      │ LISTEN/   │ Key-   │ Pub/Sub   │ Event    │ Poll
   │ Events  │ NOTIFY or │ space  │ notifs    │ Grid     │ (scheduled)
   │ → SQS   │ Debezium  │ notifs │           │          │
   │         │           │        │           │          │
   ▼         ▼           ▼        ▼           ▼          ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Per-System Bridge Processes                      │
│  Translate native events into a common StorageBridgeEvent envelope  │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                    common event envelope
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         Pub/Sub Topic                               │
│         (Google Cloud Pub/Sub, AWS SNS/SQS, or NATS)               │
│   relayfile.storage.events.{workspace_id}                           │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Storage Adapter Worker                           │
│  Subscribes to pub/sub, translates to relayfile webhook envelopes,  │
│  calls POST /v1/workspaces/{id}/webhooks/ingest                     │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────────┐
│                          Relayfile                                  │
│   Envelope queue → workspace file store → WebSocket fan-out         │
│   Agents see changes within seconds via existing event model        │
└─────────────────────────────────────────────────────────────────────┘
```

Nango's scheduled sync fits into this picture as a **secondary path** for systems where real-time bridge events are not configured. Nango polls on a schedule, detects changes, and fires a sync-complete webhook that flows through the same storage adapter worker.

---

## Latency Tiers

Different backends support different freshness levels. Operators choose a tier per source when configuring a workspace.

| Tier | Mechanism | Typical latency | Cost |
|---|---|---|---|
| **Real-time** | Native event notifications → pub/sub | < 5s | Higher infra complexity |
| **Near-real-time** | Nango sync on short schedule (1–5 min) | 1–5 min | Low, uses existing Nango |
| **Scheduled** | Nango sync on longer schedule (15–60 min) | 15–60 min | Lowest |
| **Pull-only** | No bridge; agents query directly | On-demand | No bridge needed |

The spec covers real-time and near-real-time tiers. Pull-only is the existing behavior with no storage bridge.

---

## Common Event Envelope

All per-system bridges emit the same `StorageBridgeEvent` before publishing to the pub/sub topic. This decouples the bridge implementations from the adapter.

```typescript
interface StorageBridgeEvent {
  // Unique ID for deduplication. Bridge generates if source doesn't provide one.
  eventId: string;

  // ISO 8601 timestamp of when the change occurred at the source.
  occurredAt: string;

  // ISO 8601 timestamp of when the bridge detected the change.
  detectedAt: string;

  // Which storage system emitted this event.
  source: "s3" | "postgres" | "redis" | "gcs" | "azure_blob" | "sftp" | "local_fs";

  // Change type.
  changeType: "created" | "updated" | "deleted";

  // The logical path this event maps to in the relayfile workspace.
  // Bridge computes this from the source-specific resource identifier.
  // Example: "s3://my-bucket/reports/q1.csv" → "/s3/my-bucket/reports/q1.csv"
  relayfilePath: string;

  // Raw resource identifier from the source system.
  resourceId: string;

  // Byte size of the object. Null for deletes or when unknown.
  sizeBytes: number | null;

  // Content fingerprint (ETag, content hash, or LSN). Used for deduplication
  // and to detect whether content actually changed vs. metadata-only updates.
  fingerprint: string | null;

  // Source-specific metadata passed through to relayfile semantics.properties.
  metadata: Record<string, string>;

  // Workspace this event is destined for.
  workspaceId: string;
}
```

### Example: S3 object created

```json
{
  "eventId": "s3-evt-us-east-1-my-bucket-reports-q1-csv-1746787200",
  "occurredAt": "2026-05-09T10:00:00Z",
  "detectedAt": "2026-05-09T10:00:02Z",
  "source": "s3",
  "changeType": "created",
  "relayfilePath": "/s3/my-bucket/reports/q1.csv",
  "resourceId": "s3://my-bucket/reports/q1.csv",
  "sizeBytes": 204800,
  "fingerprint": "d41d8cd98f00b204e9800998ecf8427e",
  "metadata": {
    "s3.bucket": "my-bucket",
    "s3.key": "reports/q1.csv",
    "s3.region": "us-east-1",
    "s3.storage_class": "STANDARD",
    "s3.version_id": "BCs11abcd"
  },
  "workspaceId": "ws_acme"
}
```

---

## File Path Conventions

Storage bridges map source-specific resource identifiers to relayfile paths. Rules:

- Top-level directory is the source system name: `/s3/`, `/postgres/`, `/redis/`, `/gcs/`, `/azure/`
- Next segment identifies the resource container (bucket, database+table, keyspace, etc.)
- Remaining segments mirror the source hierarchy
- Files always carry an extension reflecting content type (`.csv`, `.json`, `.parquet`, `.txt`)
- Postgres rows and Redis hashes are serialized as `.json`

| Source | Resource | Path pattern | Example |
|---|---|---|---|
| S3 | Object | `/s3/{bucket}/{key}` | `/s3/my-bucket/reports/q1.csv` |
| Postgres | Row | `/postgres/{db}/{schema}/{table}/{pk}.json` | `/postgres/prod/public/orders/48291.json` |
| Redis | String key | `/redis/{db}/{key}` | `/redis/0/session:user:42` |
| Redis | Hash | `/redis/{db}/{key}.json` | `/redis/0/user:42.json` |
| Redis | Sorted set member | `/redis/{db}/{key}/{member}` | `/redis/0/leaderboard/alice` |
| GCS | Object | `/gcs/{bucket}/{name}` | `/gcs/data-lake/exports/2026-05-09.parquet` |
| Azure Blob | Blob | `/azure/{account}/{container}/{name}` | `/azure/mystg/raw/ingest/file.json` |
| SFTP | File | `/sftp/{host}/{path}` | `/sftp/ftp.vendor.com/exports/data.csv` |

---

## Per-System Bridge Specifications

### S3

**Real-time path:** S3 Event Notifications → SQS → bridge process

S3 natively emits object-level events (create, delete, restore) to SNS/SQS/Lambda on object operations. No additional infrastructure is needed on the storage side — the bucket is configured once.

#### Setup

```
S3 Bucket
  └── Event Notifications
        ├── ObjectCreated:* → SQS queue: relayfile-s3-{bucket}-events
        ├── ObjectRemoved:* → SQS queue: relayfile-s3-{bucket}-events
        └── ObjectRestore:* → SQS queue: relayfile-s3-{bucket}-events
```

SQS queue policy allows S3 to publish. Bridge process polls the SQS queue (long polling, 20s wait) and translates each S3 event record into a `StorageBridgeEvent`.

#### S3 Event → StorageBridgeEvent mapping

```typescript
function mapS3Event(record: S3EventRecord, workspaceId: string): StorageBridgeEvent {
  const changeType =
    record.eventName.startsWith("ObjectCreated") ? "created" :
    record.eventName.startsWith("ObjectRemoved") ? "deleted" : "updated";

  return {
    eventId: record.responseElements?.["x-amz-request-id"] ?? uuid(),
    occurredAt: record.eventTime,
    detectedAt: new Date().toISOString(),
    source: "s3",
    changeType,
    relayfilePath: `/s3/${record.s3.bucket.name}/${record.s3.object.key}`,
    resourceId: `s3://${record.s3.bucket.name}/${record.s3.object.key}`,
    sizeBytes: record.s3.object.size ?? null,
    fingerprint: record.s3.object.eTag ?? null,
    metadata: {
      "s3.bucket": record.s3.bucket.name,
      "s3.key": record.s3.object.key,
      "s3.region": record.awsRegion,
      "s3.version_id": record.s3.object.versionId ?? "",
      "s3.event_name": record.eventName,
    },
    workspaceId,
  };
}
```

#### SQS bridge worker

```typescript
async function runS3Bridge(config: S3BridgeConfig): Promise<void> {
  const sqs = new SQSClient({ region: config.region });
  while (true) {
    const result = await sqs.send(new ReceiveMessageCommand({
      QueueUrl: config.queueUrl,
      MaxNumberOfMessages: 10,
      WaitTimeSeconds: 20,
    }));
    for (const message of result.Messages ?? []) {
      const body = JSON.parse(message.Body!);
      const s3Event: S3Event = JSON.parse(body.Message ?? body); // unwrap SNS if needed
      for (const record of s3Event.Records ?? []) {
        const event = mapS3Event(record, config.workspaceId);
        await publishToPubSub(event, config.pubSubTopic);
      }
      await sqs.send(new DeleteMessageCommand({
        QueueUrl: config.queueUrl,
        ReceiptHandle: message.ReceiptHandle!,
      }));
    }
  }
}
```

#### Nango fallback path

For S3 buckets where Event Notifications cannot be configured (third-party buckets, cross-account scenarios), Nango runs a scheduled sync using the S3 ListObjectsV2 API with a stored cursor (last-modified timestamp). Nango fires a sync-complete webhook with added/updated/deleted object lists. Latency: 1–5 min depending on sync interval.

#### Deduplication

S3 Event Notifications are at-least-once. The bridge uses the SQS message deduplication ID (for FIFO queues) or the `x-amz-request-id` from the event record as the `eventId`. The relayfile ingest endpoint deduplicates by `deliveryId`.

---

### Postgres

**Real-time path:** `LISTEN/NOTIFY` bridge or Debezium (logical replication)

Two options depending on operational context:

**Option A: `LISTEN/NOTIFY` (lightweight)**
Works for row-level changes where the application already fires triggers or the team is willing to add them. Low operational overhead, no extra infrastructure.

**Option B: Debezium (robust)**
Uses Postgres logical replication to capture every write without application-level triggers. Requires `wal_level = logical` in Postgres config. More operationally complex but captures all changes including bulk imports and migrations.

#### Option A: LISTEN/NOTIFY bridge

A Postgres trigger on each watched table fires `NOTIFY` with a JSON payload:

```sql
CREATE OR REPLACE FUNCTION relayfile_notify_change()
RETURNS trigger AS $$
DECLARE
  payload json;
BEGIN
  payload := json_build_object(
    'schema', TG_TABLE_SCHEMA,
    'table',  TG_TABLE_NAME,
    'op',     TG_OP,           -- INSERT, UPDATE, DELETE
    'pk',     row_to_json(NEW).id  -- assumes id column; configurable
  );
  PERFORM pg_notify('relayfile_changes', payload::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER relayfile_orders_change
AFTER INSERT OR UPDATE OR DELETE ON orders
FOR EACH ROW EXECUTE FUNCTION relayfile_notify_change();
```

The bridge process connects to Postgres, executes `LISTEN relayfile_changes`, and processes notifications:

```typescript
async function runPostgresListenBridge(config: PostgresBridgeConfig): Promise<void> {
  const client = new pg.Client(config.connectionString);
  await client.connect();
  await client.query("LISTEN relayfile_changes");

  client.on("notification", async (msg) => {
    const payload = JSON.parse(msg.payload!);
    const row = await fetchRow(client, payload.schema, payload.table, payload.pk);
    const event: StorageBridgeEvent = {
      eventId: `pg-${payload.schema}-${payload.table}-${payload.pk}-${Date.now()}`,
      occurredAt: new Date().toISOString(),
      detectedAt: new Date().toISOString(),
      source: "postgres",
      changeType: payload.op === "INSERT" ? "created" : payload.op === "DELETE" ? "deleted" : "updated",
      relayfilePath: `/postgres/${config.database}/${payload.schema}/${payload.table}/${payload.pk}.json`,
      resourceId: `postgres://${config.database}/${payload.schema}/${payload.table}/${payload.pk}`,
      sizeBytes: null,
      fingerprint: null,
      metadata: {
        "postgres.schema": payload.schema,
        "postgres.table": payload.table,
        "postgres.pk": String(payload.pk),
        "postgres.op": payload.op,
        "postgres.database": config.database,
      },
      workspaceId: config.workspaceId,
    };
    await publishToPubSub(event, config.pubSubTopic);
  });

  // Reconnect on connection loss
  client.on("error", async () => {
    await sleep(5000);
    runPostgresListenBridge(config); // restart
  });
}
```

#### Option B: Debezium (logical replication)

Debezium connects as a Postgres replication slot and streams WAL events to Kafka or Pub/Sub directly. A thin adapter subscribes to the Debezium output topic and translates to `StorageBridgeEvent`.

Debezium connector config:

```json
{
  "name": "relayfile-postgres-connector",
  "config": {
    "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
    "database.hostname": "postgres.internal",
    "database.port": "5432",
    "database.user": "relayfile_replication",
    "database.password": "${POSTGRES_REPLICATION_PASSWORD}",
    "database.dbname": "prod",
    "database.server.name": "prod",
    "table.include.list": "public.orders,public.customers,public.products",
    "plugin.name": "pgoutput",
    "slot.name": "relayfile_slot",
    "publication.name": "relayfile_pub",
    "topic.prefix": "relayfile.postgres.prod"
  }
}
```

Postgres setup:

```sql
-- Requires superuser or replication role
CREATE PUBLICATION relayfile_pub FOR TABLE orders, customers, products;
-- Debezium creates the slot automatically on first connect
```

#### Primary key handling

Postgres tables may have composite primary keys or non-integer PKs. The bridge config specifies the PK column(s) per table:

```yaml
tables:
  - schema: public
    table: orders
    pk_columns: [id]
  - schema: public
    table: order_items
    pk_columns: [order_id, line_item_id]  # composite → joined with "-"
```

For composite PKs, the relayfile path uses a joined key: `/postgres/prod/public/order_items/5001-3.json`.

#### Content serialization

Rows are serialized as JSON. Sensitive columns can be excluded per table in the bridge config. Arrays and JSONB columns are inlined. Large text/blob columns over 1 MB are truncated and a `_truncated: true` flag is added to the JSON.

#### Nango fallback path

Nango can poll Postgres via a SQL query with a `WHERE updated_at > $last_synced_at` cursor. This requires an `updated_at` column on each watched table. Suitable for tables without trigger support or when Debezium cannot be deployed. Latency: per Nango sync interval.

---

### Redis

**Real-time path:** Keyspace notifications → bridge process → pub/sub

Redis keyspace notifications emit events for key-level operations (`SET`, `DEL`, `EXPIRE`, `HSET`, etc.) over Redis pub/sub channels. A bridge process subscribes, fetches the current value of changed keys, and emits `StorageBridgeEvent`.

#### Redis configuration

```
# redis.conf
notify-keyspace-events KEA
# K = keyspace events (channel: __keyspace@{db}__:{key})
# E = keyevent events  (channel: __keyevent@{db}__:{op})
# A = all commands (alias for g$lzxetd)
```

Only enable the specific event types needed to reduce noise:
- `Kg$` — generic commands (SET, DEL, EXPIRE, RENAME)
- `Kh` — hash commands (HSET, HDEL)
- `Kz` — sorted set commands (ZADD, ZREM)

#### Bridge process

```typescript
async function runRedisBridge(config: RedisBridgeConfig): Promise<void> {
  const subscriber = new Redis(config.connectionString);
  const reader = new Redis(config.connectionString);

  // Subscribe to keyevent notifications for specified databases and key patterns
  for (const db of config.databases) {
    await subscriber.psubscribe(`__keyevent@${db}__:*`);
  }

  subscriber.on("pmessage", async (_pattern, channel, operation) => {
    // channel: __keyevent@0__:set  → extract db and op
    const dbMatch = channel.match(/__keyevent@(\d+)__:/);
    if (!dbMatch) return;
    const db = dbMatch[1];

    // keyevent channels don't carry the key — need a second channel or scan
    // Use __keyspace@{db}__:{key} pattern instead for key-level granularity
  });

  // Prefer keyspace channel which carries the key name
  for (const db of config.databases) {
    await subscriber.psubscribe(`__keyspace@${db}__:*`);
  }

  subscriber.on("pmessage", async (_pattern, channel, operation) => {
    const match = channel.match(/__keyspace@(\d+)__:(.+)/);
    if (!match) return;
    const [, db, key] = match;

    if (!config.keyPatterns.some(p => minimatch(key, p))) return;

    const changeType: StorageBridgeEvent["changeType"] =
      operation === "del" || operation === "expired" ? "deleted" : "updated";

    let value: string | null = null;
    let sizeBytes: number | null = null;

    if (changeType !== "deleted") {
      const type = await reader.type(key);
      if (type === "string") {
        value = await reader.get(key);
      } else if (type === "hash") {
        value = JSON.stringify(await reader.hgetall(key));
      } else if (type === "zset") {
        value = JSON.stringify(await reader.zrangebyscore(key, "-inf", "+inf", "WITHSCORES"));
      }
      sizeBytes = value ? Buffer.byteLength(value, "utf8") : null;
    }

    const extension = (await reader.type(key)) === "hash" ? ".json" : "";
    const event: StorageBridgeEvent = {
      eventId: `redis-${db}-${key}-${operation}-${Date.now()}`,
      occurredAt: new Date().toISOString(),
      detectedAt: new Date().toISOString(),
      source: "redis",
      changeType,
      relayfilePath: `/redis/${db}/${key}${extension}`,
      resourceId: `redis://${config.host}/${db}/${key}`,
      sizeBytes,
      fingerprint: value ? sha256(value) : null,
      metadata: {
        "redis.db": db,
        "redis.key": key,
        "redis.operation": operation,
        "redis.host": config.host,
      },
      workspaceId: config.workspaceId,
    };
    await publishToPubSub(event, config.pubSubTopic);
  });
}
```

#### Limitations

- Keyspace notifications are best-effort — Redis may drop them under memory pressure if `maxmemory-policy` evicts keys before the notification is processed.
- Large values (> 1 MB) are truncated in the serialized file.
- Redis cluster mode: each shard must be subscribed to independently.
- No Nango fallback for Redis — Nango does not have a Redis integration. Schedule-tier support would require a custom poll script.

---

### GCS (Google Cloud Storage)

**Real-time path:** GCS Pub/Sub notifications → bridge process

GCS supports native Pub/Sub notifications triggered on object create, delete, metadata update, and archive events. This is the cleanest integration of all the storage backends.

#### Setup

```bash
# Create notification on bucket
gcloud storage buckets notifications create \
  gs://my-bucket \
  --topic=projects/my-project/topics/relayfile-gcs-events \
  --event-types=OBJECT_FINALIZE,OBJECT_DELETE,OBJECT_METADATA_UPDATE \
  --payload-format=JSON_API_V1

# Grant GCS permission to publish to topic
gcloud pubsub topics add-iam-policy-binding relayfile-gcs-events \
  --member=serviceAccount:service-{PROJECT_NUMBER}@gs-project-accounts.iam.gserviceaccount.com \
  --role=roles/pubsub.publisher
```

#### GCS message → StorageBridgeEvent mapping

```typescript
function mapGCSMessage(msg: GCSPubSubMessage, workspaceId: string): StorageBridgeEvent {
  const attrs = msg.attributes;
  const changeType: StorageBridgeEvent["changeType"] =
    attrs.eventType === "OBJECT_FINALIZE" ? (attrs.overwroteGeneration ? "updated" : "created") :
    attrs.eventType === "OBJECT_DELETE" ? "deleted" : "updated";

  const payload = JSON.parse(Buffer.from(msg.data, "base64").toString());

  return {
    eventId: `gcs-${attrs.bucketId}-${attrs.objectId}-${attrs.eventTime}`,
    occurredAt: attrs.eventTime,
    detectedAt: new Date().toISOString(),
    source: "gcs",
    changeType,
    relayfilePath: `/gcs/${attrs.bucketId}/${attrs.objectId}`,
    resourceId: `gcs://${attrs.bucketId}/${attrs.objectId}`,
    sizeBytes: payload.size ? parseInt(payload.size) : null,
    fingerprint: payload.md5Hash ?? payload.etag ?? null,
    metadata: {
      "gcs.bucket": attrs.bucketId,
      "gcs.object": attrs.objectId,
      "gcs.generation": attrs.objectGeneration ?? "",
      "gcs.content_type": payload.contentType ?? "",
      "gcs.storage_class": payload.storageClass ?? "",
    },
    workspaceId,
  };
}
```

#### Bridge worker

```typescript
async function runGCSBridge(config: GCSBridgeConfig): Promise<void> {
  const pubsub = new PubSub({ projectId: config.projectId });
  const subscription = pubsub.subscription(config.subscriptionName);

  subscription.on("message", async (message) => {
    try {
      const event = mapGCSMessage(message as GCSPubSubMessage, config.workspaceId);
      await publishToPubSub(event, config.pubSubTopic);
      message.ack();
    } catch (err) {
      console.error("GCS bridge error", err);
      message.nack(); // redelivery
    }
  });

  subscription.on("error", (err) => {
    console.error("GCS subscription error", err);
    // PubSub client auto-reconnects
  });
}
```

GCS Pub/Sub delivery is at-least-once with a 7-day retention window. The bridge acks after successfully publishing to the relay pub/sub topic; nacks on failure trigger redelivery.

---

### Azure Blob Storage

**Real-time path:** Azure Event Grid → Event Hub or webhook → bridge process

Azure Blob emits events via Event Grid for blob created, deleted, and renamed operations. Event Grid routes to an Event Hub (for high-volume), a webhook endpoint, or a storage queue.

#### Setup

```bash
# Register Event Grid resource provider
az provider register --namespace Microsoft.EventGrid

# Create Event Grid subscription on storage account
az eventgrid event-subscription create \
  --name relayfile-blob-events \
  --source-resource-id /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Storage/storageAccounts/{account} \
  --endpoint-type eventhub \
  --endpoint /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.EventHub/namespaces/{ns}/eventhubs/relayfile-blob-events \
  --included-event-types Microsoft.Storage.BlobCreated Microsoft.Storage.BlobDeleted
```

#### Azure Event Grid → StorageBridgeEvent mapping

```typescript
function mapAzureBlobEvent(event: EventGridEvent, workspaceId: string): StorageBridgeEvent {
  const data = event.data as AzureBlobEventData;
  const url = new URL(data.url);
  // url.pathname: /{container}/{blobName}
  const [, container, ...blobParts] = url.pathname.split("/");
  const blobName = blobParts.join("/");
  const account = url.hostname.split(".")[0];

  return {
    eventId: event.id,
    occurredAt: event.eventTime,
    detectedAt: new Date().toISOString(),
    source: "azure_blob",
    changeType: event.eventType === "Microsoft.Storage.BlobCreated" ? "created" : "deleted",
    relayfilePath: `/azure/${account}/${container}/${blobName}`,
    resourceId: data.url,
    sizeBytes: data.contentLength ?? null,
    fingerprint: data.eTag ?? null,
    metadata: {
      "azure.account": account,
      "azure.container": container,
      "azure.blob": blobName,
      "azure.content_type": data.contentType ?? "",
      "azure.blob_type": data.blobType ?? "",
    },
    workspaceId,
  };
}
```

---

## Nango Scheduled Sync Integration

For backends where real-time event bridges are not deployed, Nango's scheduled sync provides a near-real-time alternative. The flow:

```
Nango sync runs on schedule
  │  detects added/updated/deleted objects since last cursor
  ▼
Nango fires sync-complete webhook to storage adapter worker
  │
  ▼
Adapter worker translates each changed object into a StorageBridgeEvent
  │  (source: "s3" | "postgres" etc., changeType per Nango's delta)
  ▼
Events published to pub/sub topic
  │
  ▼
Same storage adapter worker picks them up and ingests into relayfile
```

### Nango sync webhook payload

Nango delivers this payload to the configured webhook endpoint when a sync run completes:

```json
{
  "connectionId": "conn_s3_acme",
  "providerConfigKey": "s3",
  "syncName": "s3-objects",
  "model": "S3Object",
  "responseResults": {
    "added": 12,
    "updated": 3,
    "deleted": 1
  },
  "syncType": "INCREMENTAL",
  "queryTimeStamp": "2026-05-09T10:05:00Z",
  "records": [
    {
      "_nango_metadata": { "action": "ADDED", "cursor": "2026-05-09T10:04:58Z" },
      "key": "reports/q1.csv",
      "bucket": "my-bucket",
      "size": 204800,
      "etag": "d41d8cd98f00b204e9800998ecf8427e",
      "lastModified": "2026-05-09T10:04:58Z",
      "contentType": "text/csv"
    }
  ]
}
```

### Nango webhook → StorageBridgeEvent mapping

```typescript
function mapNangoSyncRecord(
  record: NangoSyncRecord,
  meta: NangoSyncWebhook,
  workspaceId: string
): StorageBridgeEvent {
  const action = record._nango_metadata.action;
  const changeType: StorageBridgeEvent["changeType"] =
    action === "ADDED" ? "created" :
    action === "DELETED" ? "deleted" : "updated";

  // Source-specific path computation based on providerConfigKey
  const relayfilePath = computeRelayfilePath(meta.providerConfigKey, record);

  return {
    eventId: `nango-${meta.connectionId}-${meta.syncName}-${record.key ?? record.id}-${meta.queryTimeStamp}`,
    occurredAt: record.lastModified ?? meta.queryTimeStamp,
    detectedAt: new Date().toISOString(),
    source: mapNangoProviderToSource(meta.providerConfigKey),
    changeType,
    relayfilePath,
    resourceId: computeResourceId(meta.providerConfigKey, record),
    sizeBytes: record.size ?? null,
    fingerprint: record.etag ?? null,
    metadata: {
      "nango.connection_id": meta.connectionId,
      "nango.sync_name": meta.syncName,
      "nango.model": meta.model,
      "nango.sync_type": meta.syncType,
    },
    workspaceId,
  };
}
```

### Nango sync configuration per backend

```yaml
# nango integrations for storage backends
integrations:
  s3:
    syncs:
      s3-objects:
        runs: every 2 minutes
        output: S3Object
        endpoint: GET /objects
        track_deletes: true
        cursor_field: lastModified

  postgres:
    syncs:
      postgres-rows:
        runs: every 1 minute
        output: PostgresRow
        endpoint: GET /rows
        track_deletes: true
        cursor_field: updated_at   # requires updated_at column

  gcs:
    syncs:
      gcs-objects:
        runs: every 2 minutes
        output: GCSObject
        endpoint: GET /objects
        track_deletes: true
```

---

## Storage Adapter Worker

The storage adapter worker is a single process that:
1. Subscribes to the pub/sub topic for `StorageBridgeEvent` messages
2. Fetches file content from the source system if not included in the event
3. Translates to a relayfile webhook envelope
4. Calls `POST /v1/workspaces/{id}/webhooks/ingest`

### Content fetching

Most bridge events include metadata but not file content. The adapter fetches content on demand before ingestion:

```typescript
async function fetchContent(event: StorageBridgeEvent, config: AdapterConfig): Promise<Buffer | null> {
  if (event.changeType === "deleted") return null;

  switch (event.source) {
    case "s3": {
      const s3 = new S3Client({ region: config.s3.region });
      const [, bucket, ...keyParts] = event.relayfilePath.split("/").slice(1);
      const result = await s3.send(new GetObjectCommand({
        Bucket: bucket,
        Key: keyParts.join("/"),
      }));
      return Buffer.from(await result.Body!.transformToByteArray());
    }
    case "gcs": {
      const storage = new Storage();
      const [, bucket, ...nameParts] = event.relayfilePath.split("/").slice(1);
      const [content] = await storage.bucket(bucket).file(nameParts.join("/")).download();
      return content;
    }
    case "postgres": {
      // Content was fetched by the bridge process and included in event metadata
      return Buffer.from(event.metadata["postgres.row_json"] ?? "null");
    }
    case "redis": {
      // Content was fetched by the bridge process and included in event metadata
      return Buffer.from(event.metadata["redis.value"] ?? "null");
    }
    case "azure_blob": {
      const client = new BlobServiceClient(config.azure.connectionString);
      const [, account, container, ...nameParts] = event.relayfilePath.split("/").slice(1);
      const containerClient = client.getContainerClient(container);
      const blobClient = containerClient.getBlobClient(nameParts.join("/"));
      const download = await blobClient.download();
      return Buffer.concat(await streamToBuffers(download.readableStreamBody!));
    }
  }
  return null;
}
```

### Webhook envelope construction

```typescript
async function ingestEvent(
  event: StorageBridgeEvent,
  content: Buffer | null,
  relayfileClient: RelayFileClient
): Promise<void> {
  const envelope: IngestWebhookInput = {
    provider: event.source,
    event_type: event.changeType === "created" ? "file.created" :
                event.changeType === "deleted" ? "file.deleted" : "file.updated",
    path: event.relayfilePath,
    delivery_id: event.eventId,
    timestamp: event.occurredAt,
    data: content ? {
      content: content.toString("base64"),
      encoding: "base64",
      content_type: inferContentType(event.relayfilePath),
    } : null,
    headers: {
      "X-Storage-Source": event.source,
      "X-Storage-Resource-Id": event.resourceId,
    },
  };

  // Merge source-specific metadata into semantics properties
  const properties: Record<string, string> = {
    "storage.source": event.source,
    "storage.resource_id": event.resourceId,
    "storage.fingerprint": event.fingerprint ?? "",
    "storage.size_bytes": String(event.sizeBytes ?? ""),
    ...event.metadata,
  };

  await relayfileClient.ingestWebhook(event.workspaceId, {
    ...envelope,
    semantics: { properties },
  });
}
```

---

## Pub/Sub Layer

The pub/sub topic is the decoupling point between per-system bridges and the storage adapter worker. Any pub/sub implementation works; the bridge and adapter only depend on the `StorageBridgeEvent` envelope.

### Supported backends

| Pub/Sub system | When to use |
|---|---|
| **Google Cloud Pub/Sub** | GCP deployments; native for GCS bridge |
| **AWS SQS** | AWS deployments; native for S3 bridge |
| **NATS JetStream** | Self-hosted; low latency, simple ops |
| **Redis Streams** | Already running Redis; zero additional infra |
| **Kafka** | Already running Kafka (e.g., Debezium output) |

### Topic naming convention

```
relayfile.storage.events.{workspace_id}
```

One topic per workspace. Bridge processes publish to the workspace topic. The adapter worker subscribes per workspace. For multi-tenant deployments, a single topic fan-out with workspace-level filtering is also acceptable.

### Message format on the wire

```json
{
  "messageId": "pub-sub-native-id",
  "publishTime": "2026-05-09T10:00:02Z",
  "data": "<base64-encoded StorageBridgeEvent JSON>",
  "attributes": {
    "source": "s3",
    "workspaceId": "ws_acme",
    "changeType": "created"
  }
}
```

Attributes allow the subscriber to filter by source or changeType without deserializing the full payload.

---

## Semantic Properties

All storage bridge files carry standard `semantics.properties` in the relayfile workspace:

```json
{
  "semantics": {
    "properties": {
      "storage.source": "s3",
      "storage.resource_id": "s3://my-bucket/reports/q1.csv",
      "storage.fingerprint": "d41d8cd98f00b204e9800998ecf8427e",
      "storage.size_bytes": "204800",
      "storage.last_seen_at": "2026-05-09T10:00:02Z",
      "s3.bucket": "my-bucket",
      "s3.key": "reports/q1.csv",
      "s3.region": "us-east-1",
      "s3.storage_class": "STANDARD"
    }
  }
}
```

Agents can query across all storage bridge files with:

```
GET /v1/workspaces/{id}/fs/query?property=storage.source:s3
GET /v1/workspaces/{id}/fs/query?property=s3.bucket:my-bucket
```

---

## Writeback

Writeback from relayfile to infrastructure storage backends is simpler than to SaaS providers — most storage systems have straightforward PUT/DELETE semantics with no field-level mapping.

### S3 writeback

```typescript
async function writebackToS3(item: WritebackItem, config: S3WritebackConfig): Promise<void> {
  const s3 = new S3Client({ region: config.region });
  const [, bucket, ...keyParts] = item.path.split("/").slice(1); // strip /s3/
  const key = keyParts.join("/");

  if (item.operation === "delete") {
    await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: key }));
  } else {
    await s3.send(new PutObjectCommand({
      Bucket: bucket,
      Key: key,
      Body: Buffer.from(item.content, "base64"),
      ContentType: item.contentType,
    }));
  }
}
```

### Postgres writeback

Postgres writeback requires an upsert. The adapter extracts the PK from the file path and performs an `INSERT ... ON CONFLICT DO UPDATE`:

```typescript
async function writebackToPostgres(item: WritebackItem, config: PostgresWritebackConfig): Promise<void> {
  // path: /postgres/{db}/{schema}/{table}/{pk}.json
  const [, , schema, table, pkFile] = item.path.split("/").slice(1);
  const pk = pkFile.replace(".json", "");
  const tableConfig = config.tables.find(t => t.schema === schema && t.table === table);
  if (!tableConfig) throw new Error(`No writeback config for ${schema}.${table}`);

  const row = JSON.parse(Buffer.from(item.content, "base64").toString());
  const pkColumn = tableConfig.pk_columns[0]; // simplified; composite PK needs expansion

  await config.client.query(
    `INSERT INTO ${schema}.${table} SELECT * FROM json_populate_record(null::${schema}.${table}, $1)
     ON CONFLICT (${pkColumn}) DO UPDATE SET ${
       Object.keys(row).filter(k => k !== pkColumn).map(k => `${k} = EXCLUDED.${k}`).join(", ")
     }`,
    [JSON.stringify(row)]
  );
}
```

### Redis writeback

```typescript
async function writebackToRedis(item: WritebackItem, config: RedisWritebackConfig): Promise<void> {
  // path: /redis/{db}/{key} or /redis/{db}/{key}.json
  const [, , db, ...keyParts] = item.path.split("/").slice(1);
  const key = keyParts.join("/").replace(/\.json$/, "");
  const redis = config.clients[parseInt(db)];

  if (item.operation === "delete") {
    await redis.del(key);
  } else {
    const value = Buffer.from(item.content, "base64").toString();
    if (item.path.endsWith(".json")) {
      // Hash type
      const hash = JSON.parse(value);
      await redis.hset(key, hash);
    } else {
      await redis.set(key, value);
    }
  }
}
```

Writeback is optional per source and requires explicit `writeback_enabled: true` in the bridge config. Destructive writebacks (Postgres row delete, Redis key delete) require a separate `writeback_destructive: true` flag.

---

## Error Handling and Reliability

### Bridge process reliability

| Failure | Behavior | Recovery |
|---|---|---|
| Source system unavailable | Bridge retries with exponential backoff (1s, 2s, 4s… up to 60s) | Auto-recovers when source is available |
| Pub/sub publish fails | Bridge retries up to 5 times, then logs and skips | Next event will be processed; missed event caught by Nango schedule if configured |
| Pub/sub unavailable | Bridge buffers up to 10,000 events in memory; drains when reconnected | Events older than buffer age are lost; Nango schedule provides eventual catch-up |
| Bridge process crash | Events since last checkpoint are re-processed on restart (at-least-once) | Relayfile deduplicates by `deliveryId` |
| Content fetch fails | Event is published with `sizeBytes: null` and `fingerprint: null`; adapter re-fetches | Adapter retries content fetch up to 3 times before acking as failed |

### Adapter worker reliability

| Failure | Behavior | Recovery |
|---|---|---|
| Relayfile ingest returns 429 | Adapter respects `Retry-After`, re-queues message | Pub/sub redelivers after nack |
| Relayfile ingest returns 409 (duplicate) | Adapter acks and discards — idempotent | No action needed |
| Relayfile unavailable | Adapter nacks; pub/sub holds message for retention period | Auto-recovers; messages delivered when relayfile is back |
| Content fetch from source times out | Adapter nacks for redelivery | Retried up to 3 times; dead-lettered after max attempts |

### Dead-letter handling

Messages that fail after max retry attempts are routed to a dead-letter topic:

```
relayfile.storage.deadletter.{workspace_id}
```

Operators subscribe to this topic for alerting. A separate reconciliation job runs every 6 hours to re-process dead-lettered events using the Nango fallback path.

---

## Configuration Schema

```yaml
# storage-bridge-config.yaml
workspace_id: ws_acme
relayfile_url: https://relayfile.internal
relayfile_token: ${RELAYFILE_WORKSPACE_TOKEN}

pubsub:
  backend: google_cloud_pubsub  # | aws_sqs | nats | redis_streams
  project_id: my-gcp-project
  topic: relayfile.storage.events.ws_acme
  subscription: relayfile-storage-adapter-ws-acme

sources:
  - type: s3
    enabled: true
    tier: real_time  # | nango_scheduled
    region: us-east-1
    buckets:
      - name: my-bucket
        sqs_queue_url: https://sqs.us-east-1.amazonaws.com/123456789/relayfile-s3-my-bucket-events
        writeback_enabled: true
        writeback_destructive: false

  - type: postgres
    enabled: true
    tier: real_time
    connection_string: ${POSTGRES_CONNECTION_STRING}
    database: prod
    bridge_mode: listen_notify  # | debezium
    tables:
      - schema: public
        table: orders
        pk_columns: [id]
        writeback_enabled: true
      - schema: public
        table: customers
        pk_columns: [id]
        writeback_enabled: false
        excluded_columns: [password_hash, ssn]

  - type: redis
    enabled: true
    tier: real_time
    host: redis.internal
    port: 6379
    databases: [0, 1]
    key_patterns:
      - "user:*"
      - "session:*"
      - "order:*"
    writeback_enabled: false

  - type: gcs
    enabled: true
    tier: real_time
    project_id: my-gcp-project
    subscription: projects/my-gcp-project/subscriptions/relayfile-gcs-events
    writeback_enabled: true

  - type: azure_blob
    enabled: false
    tier: nango_scheduled
    nango_connection_id: conn_azure_acme
    writeback_enabled: false

nango:
  base_url: https://api.nango.dev
  secret_key: ${NANGO_SECRET_KEY}
  webhook_secret: ${NANGO_WEBHOOK_SECRET}
  # Endpoint that receives Nango sync-complete webhooks
  webhook_receiver_url: https://bridge.internal/nango/webhook
```

---

## Observability

### Metrics

| Metric | Labels | Description |
|---|---|---|
| `storage_bridge_events_total` | `source`, `change_type`, `workspace_id` | Events received from source system |
| `storage_bridge_publish_duration_ms` | `source`, `status` | Time to publish event to pub/sub |
| `storage_bridge_publish_errors_total` | `source`, `error_type` | Failed pub/sub publishes |
| `storage_adapter_ingest_total` | `source`, `change_type`, `status` | Relayfile ingest outcomes |
| `storage_adapter_ingest_duration_ms` | `source` | End-to-end ingest latency |
| `storage_adapter_content_fetch_duration_ms` | `source` | Time to fetch file content from source |
| `storage_adapter_deadletter_total` | `source` | Events routed to dead-letter |
| `storage_bridge_source_lag_seconds` | `source` | Time between event occurrence and relayfile ingest |

### Alerting thresholds

| Alert | Condition | Severity |
|---|---|---|
| High dead-letter rate | `storage_adapter_deadletter_total` > 10 in 5m | Warning |
| Ingest lag | `storage_bridge_source_lag_seconds` p99 > 30s | Warning |
| Ingest lag severe | `storage_bridge_source_lag_seconds` p99 > 120s | Critical |
| Bridge process down | No events from source in 10m (when source is known active) | Critical |
| Pub/sub backlog | Undelivered message count > 50,000 | Warning |

### Structured logging

Each bridge event and adapter ingest is logged as a single JSON line:

```json
{
  "ts": "2026-05-09T10:00:02Z",
  "level": "info",
  "msg": "storage_event_ingested",
  "workspace_id": "ws_acme",
  "source": "s3",
  "change_type": "created",
  "relayfile_path": "/s3/my-bucket/reports/q1.csv",
  "event_id": "s3-evt-abc123",
  "occurred_at": "2026-05-09T10:00:00Z",
  "lag_ms": 2140,
  "content_size_bytes": 204800,
  "ingest_status": "ok"
}
```

---

## Security

### Authentication

- Bridge processes authenticate to the source system using service account credentials (IAM role for S3/GCS, connection string for Postgres/Redis, managed identity for Azure).
- Credentials are never stored in the bridge config file — always injected via environment variables or a secrets manager.
- The adapter worker authenticates to relayfile using a workspace-scoped JWT with `sync:trigger` scope.

### Network isolation

- Bridge processes run in the same VPC as the source systems. No public internet exposure required for ingest.
- The adapter worker communicates with relayfile over TLS. If relayfile is self-hosted in the same VPC, mTLS is recommended.
- Pub/sub topics are private; access controlled by IAM/ACL.

### Data handling

- File content transits the pub/sub topic only for small objects (< 1 MB). Larger objects are fetched directly by the adapter from the source, never passing through pub/sub.
- Excluded columns (Postgres) and excluded key patterns (Redis) are enforced in the bridge before publishing — they never reach pub/sub or relayfile.
- Writeback payloads are validated against the original file schema before forwarding to prevent injection of unexpected fields.

---

## Implementation Phases

### Phase 1 — S3 + Nango scheduled fallback

Delivers value for the most common use case immediately with minimal operational complexity.

- S3 bridge (SQS poller)
- Nango webhook receiver translating sync-complete webhooks to `StorageBridgeEvent`
- Storage adapter worker (pub/sub → relayfile ingest)
- Google Cloud Pub/Sub as the default pub/sub backend
- Basic metrics and structured logging

**Estimated effort:** 2–3 weeks

### Phase 2 — GCS + Postgres (LISTEN/NOTIFY)

- GCS bridge (native Pub/Sub subscription)
- Postgres LISTEN/NOTIFY bridge with per-table trigger setup script
- Writeback for S3 and Postgres
- Dead-letter handling and reconciliation job

**Estimated effort:** 2 weeks

### Phase 3 — Redis + Azure Blob + Debezium

- Redis keyspace notification bridge
- Azure Blob Event Grid bridge
- Debezium adapter (for Postgres teams that already run Kafka)
- Multi-pubsub-backend support (AWS SQS, NATS, Redis Streams)

**Estimated effort:** 2–3 weeks

### Phase 4 — SFTP + local filesystem + hardening

- SFTP polling bridge (no native event system; poll on schedule)
- Local filesystem bridge using inotify/FSEvents (for on-prem agent deployments)
- End-to-end latency SLO enforcement
- Chaos testing for bridge process failures

**Estimated effort:** 2 weeks

---

## Open Questions

1. **Content size limit in pub/sub**: Should the pub/sub envelope always omit content (fetched by adapter), or include it for objects under a threshold (e.g., 256 KB) to avoid a second round-trip? Tradeoff: lower latency vs. higher pub/sub message costs.

2. **Postgres schema changes**: What happens when a watched table gains or loses a column between sync runs? The adapter should detect schema drift and re-serialize rows with the new shape. Needs a schema registry or a "fetch current schema on startup" step.

3. **Redis cluster mode**: Keyspace notifications require a subscriber per shard. Do we ship a cluster-aware bridge, or document that Redis Cluster is unsupported in phase 1?

4. **Nango S3 integration**: Does Nango's S3 integration support `track_deletes: true`? If not, the Nango fallback path cannot detect S3 object deletions — only the real-time SQS path handles deletes. Need to verify with Nango docs.

5. **Writeback conflict resolution**: If an agent writes a file and the source system also updates the same object before the writeback completes, the writeback overwrites the source change. Should writeback use ETag/`If-Match` where the source system supports it (S3, GCS support conditional PUT)?

6. **Multi-workspace routing**: A single S3 bucket may serve multiple workspaces (e.g., different prefixes for different tenants). The bridge config needs a per-prefix workspace routing table, or we route all events for a bucket to a single topic and filter at the adapter.

7. **SFTP change detection**: SFTP has no event system. The only option is recursive `LIST` diffing against a stored directory snapshot. What scan interval is acceptable? 5 minutes? Configurable per source?
