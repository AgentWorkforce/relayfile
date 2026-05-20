import { createRelayfileCloudAccessTokenProvider } from "../cloud-token-provider.js"
import {
  runRelayfileCloudLogin,
  type RelayfileCloudLoginOptions,
  type RelayfileCloudTokenSet,
  type RelayfileCloudTokenSetupOptions
} from "../cloud-login.js"
import {
  defaultMountLauncher,
  readMountedWorkspaceStatus
} from "../mount-launcher.js"
import {
  RelayfileSetup as BaseRelayfileSetup
} from "../setup.js"
import type {
  MountLauncher,
  MountedWorkspaceStatus,
  ReadMountedWorkspaceStatusInput
} from "../setup-types.js"

const DEFAULT_CLOUD_API_URL = "https://agentrelay.com/cloud"

export class RelayfileSetup extends BaseRelayfileSetup {
  static override async login(
    options: RelayfileCloudLoginOptions = {}
  ): Promise<RelayfileSetup> {
    const cloudApiUrl = options.cloudApiUrl ?? DEFAULT_CLOUD_API_URL
    const tokens = await runRelayfileCloudLogin({
      ...options,
      cloudApiUrl
    })
    await options.onTokens?.({ ...tokens })
    return RelayfileSetup.fromCloudTokens(tokens, {
      ...options,
      cloudApiUrl: tokens.apiUrl ?? cloudApiUrl
    })
  }

  static override fromCloudTokens(
    tokens: RelayfileCloudTokenSet,
    options: RelayfileCloudTokenSetupOptions = {}
  ): RelayfileSetup {
    const cloudApiUrl = options.cloudApiUrl ?? tokens.apiUrl ?? DEFAULT_CLOUD_API_URL
    return new RelayfileSetup({
      ...options,
      cloudApiUrl,
      accessToken: createRelayfileCloudAccessTokenProvider(
        {
          ...tokens,
          apiUrl: tokens.apiUrl ?? cloudApiUrl
        },
        {
          ...options,
          cloudApiUrl
        }
      )
    })
  }

  protected override getDefaultMountLauncher(): MountLauncher {
    return defaultMountLauncher
  }

  protected override readMountedWorkspaceStatus(
    input: ReadMountedWorkspaceStatusInput
  ): Promise<MountedWorkspaceStatus> {
    return readMountedWorkspaceStatus(input)
  }
}
