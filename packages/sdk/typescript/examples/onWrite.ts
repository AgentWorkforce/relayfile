import { RelayFileClient, onWrite } from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.RELAYFILE_WORKSPACE_ID;

if (!token || !workspaceId) {
  throw new Error("Set RELAYFILE_TOKEN and RELAYFILE_WORKSPACE_ID before running this example.");
}

const client = new RelayFileClient({
  token,
  baseUrl: process.env.RELAYFILE_BASE_URL
});

const stopTranscriptWatcher = onWrite(
  "/notion/pages/calls/*/transcript",
  async (event) => {
    const callId = event.path.split("/")[4];
    console.log(`Call transcript changed for ${callId} at ${event.revision}`);
  },
  { client, workspaceId }
);

const stopIssueWatcher = onWrite(
  "/linear/issues/**",
  async (event) => {
    console.log(`Linear issue tree changed: ${event.path}`);
  },
  { client, workspaceId }
);

const stopPullWatcher = onWrite(
  "/github/repos/acme/api/pulls/*",
  async (event) => {
    const pullNumber = event.path.split("/").at(-1);
    console.log(`API pull request ${pullNumber} changed via ${event.source}`);
  },
  { client, workspaceId }
);

process.once("SIGINT", () => {
  stopTranscriptWatcher();
  stopIssueWatcher();
  stopPullWatcher();
});
