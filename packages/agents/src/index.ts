export { connect } from "./connect.js";
export type { ConnectOptions, RelayfileAgents } from "./connect.js";
export type { CreateResult, OpReceipt, WritebackApi } from "./writeback.js";

// Re-export SDK types + error classes so consumers have one import surface
// AND reinforce single-instance @relayfile/sdk resolution. Examples should
// import from "@relayfile/agents", never directly from "@relayfile/sdk".
export {
  RelayFileApiError,
  RevisionConflictError,
  type RelayFileClient,
  type FileReadResponse,
  type WriteQueuedResponse,
  type WriteFileInput,
  type DeleteFileInput,
} from "@relayfile/sdk";

import { vercelTools, type VercelToolsOptions } from "./tools/vercel.js";
import { openaiTools, type OpenAIToolsOptions } from "./tools/openai.js";
import { langchainTools, type LangChainToolsOptions } from "./tools/langchain.js";

export const tools = {
  vercel: vercelTools,
  openai: openaiTools,
  langchain: langchainTools,
};

export type { VercelToolsOptions, OpenAIToolsOptions, LangChainToolsOptions };
