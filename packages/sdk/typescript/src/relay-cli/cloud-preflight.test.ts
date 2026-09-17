import { describe, expect, it } from "vitest"

import {
  SETUP_INTENT,
  SETUP_INTENT_PRINTED_ENV,
  announceSetupIntent,
  hasValidSetupArguments,
  parseGoDurationMilliseconds,
  prepareCloudSession,
  shouldPrepareCloudSession,
  type EnsureCloudSessionOptions
} from "./cloud-preflight.js"

/**
 * Ported from packages/cli/scripts/cloud-preflight.test.js when the preflight
 * moved into the SDK so both `relayfile` and `agent-relay file` run the same
 * one. The bundled Cloud SDK itself is still tested where it is vendored, in
 * packages/cli/scripts/cloud-auth.test.js.
 */

describe("announceSetupIntent", () => {
  it("announces the setup intent once before authentication", () => {
    const env: NodeJS.ProcessEnv = {}
    const lines: string[] = []
    expect(announceSetupIntent([], env, (line) => lines.push(line))).toBe(true)
    expect(announceSetupIntent([], env, (line) => lines.push(line))).toBe(true)
    expect(lines).toEqual([SETUP_INTENT])
    expect(env[SETUP_INTENT_PRINTED_ENV]).toBe("1")
  })

  it("says nothing for an invocation that needs no session", () => {
    const lines: string[] = []
    expect(announceSetupIntent(["status"], {}, (line) => lines.push(line))).toBe(false)
    expect(lines).toEqual([])
  })
})

describe("prepareCloudSession", () => {
  it("prepares Cloud auth through the Agent Relay SDK for a bare invocation", async () => {
    const env: NodeJS.ProcessEnv = {}
    const calls: EnsureCloudSessionOptions[] = []
    const prepared = await prepareCloudSession([], {
      env,
      ensureCloudSession: async (options) => {
        calls.push(options)
        return {
          auth: {
            apiUrl: "https://cloud.example",
            accessToken: "cld_at_test_secret",
            refreshToken: "cld_rt_test_secret"
          }
        }
      }
    })

    expect(prepared).toBe(true)
    expect(calls[0]!.signal).toBeInstanceOf(AbortSignal)
    expect(calls[0]!.signal.aborted).toBe(false)
    expect(calls).toEqual([
      {
        apiUrl: "https://agentrelay.com/cloud",
        client: "relayfile",
        interactive: true,
        device: false,
        loginTimeoutMs: 300000,
        refreshTimeoutMs: 10000,
        signal: calls[0]!.signal
      }
    ])
    // Credentials stay in the SDK's canonical store; nothing is promoted into
    // the environment for the child process.
    expect(env).toEqual({})
  })

  it("forwards setup's Cloud URL and no-open mode to the SDK", async () => {
    let received: EnsureCloudSessionOptions | undefined
    await prepareCloudSession(
      [
        "setup",
        "--cloud-api-url=https://staging.example/cloud",
        "--no-open",
        "--login-timeout=10s"
      ],
      {
        env: {},
        ensureCloudSession: async (options) => {
          received = options
          return { auth: { apiUrl: options.apiUrl } }
        }
      }
    )

    expect(received).toEqual({
      apiUrl: "https://staging.example/cloud",
      client: "relayfile",
      interactive: true,
      device: true,
      loginTimeoutMs: 10000,
      refreshTimeoutMs: 10000,
      signal: received!.signal
    })
    expect(received!.signal).toBeInstanceOf(AbortSignal)
  })

  it("reads the Cloud URL from the environment when no flag is given", async () => {
    let received: EnsureCloudSessionOptions | undefined
    await prepareCloudSession(["setup"], {
      env: { RELAYFILE_CLOUD_API_URL: "https://env.example/cloud" },
      ensureCloudSession: async (options) => {
        received = options
      }
    })
    expect(received!.apiUrl).toBe("https://env.example/cloud")
  })

  it("keeps browser login enabled for false no-open values", async () => {
    for (const value of ["false", "0"]) {
      let received: EnsureCloudSessionOptions | undefined
      await prepareCloudSession(["setup", `--no-open=${value}`], {
        env: {},
        ensureCloudSession: async (options) => {
          received = options
        }
      })
      expect(received!.device).toBe(false)
    }
  })

  it("bounds authentication by the login timeout and aborts late writes", async () => {
    let receivedSignal: AbortSignal | undefined
    let lateCredentialWrite = false
    await expect(
      prepareCloudSession(["setup", "--login-timeout=1ms"], {
        env: {},
        ensureCloudSession: ({ signal }) => {
          receivedSignal = signal
          return new Promise<void>((resolve, reject) => {
            const lateWrite = setTimeout(() => {
              lateCredentialWrite = true
              resolve()
            }, 25)
            signal.addEventListener(
              "abort",
              () => {
                clearTimeout(lateWrite)
                reject(signal.reason)
              },
              { once: true }
            )
          })
        }
      })
    ).rejects.toThrow(/Cloud sign-in timed out after 1ms/)

    expect(receivedSignal!.aborted).toBe(true)
    expect((receivedSignal!.reason as Error).message).toMatch(/timed out after 1ms/)
    await new Promise((resolve) => setTimeout(resolve, 30))
    expect(lateCredentialWrite).toBe(false)
  })

  it("never starts auth for pseudo-help values", async () => {
    for (const args of [
      ["setup", "--help=false"],
      ["setup", "-h=0", "--cloud-token="]
    ]) {
      let calls = 0
      const prepared = await prepareCloudSession(args, {
        env: { RELAYFILE_CLOUD_TOKEN: "inherited-token" },
        ensureCloudSession: async () => {
          calls += 1
        }
      })
      expect(prepared).toBe(false)
      expect(calls).toBe(0)
    }
  })

  it("short-circuits native help even in a setup value slot", async () => {
    let calls = 0
    const prepared = await prepareCloudSession(["setup", "--provider", "--help"], {
      env: {},
      ensureCloudSession: async () => {
        calls += 1
      }
    })
    expect(prepared).toBe(false)
    expect(calls).toBe(0)
  })

  it("treats --version as a native version only when the CLI would", async () => {
    expect(shouldPrepareCloudSession(["--version"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["version"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--local-dir", "--version"], {})).toBe(true)

    let calls = 0
    const prepared = await prepareCloudSession(["setup", "--local-dir", "--version"], {
      env: {},
      ensureCloudSession: async () => {
        calls += 1
      }
    })
    expect(prepared).toBe(true)
    expect(calls).toBe(1)
  })

  it("follows the native setup grammar for dash-prefixed values", async () => {
    const args = ["setup", "--local-dir", "-mirror"]
    expect(hasValidSetupArguments(args)).toBe(true)
    let calls = 0
    const prepared = await prepareCloudSession(args, {
      env: {},
      ensureCloudSession: async () => {
        calls += 1
      }
    })
    expect(prepared).toBe(true)
    expect(calls).toBe(1)
  })

  it("rejects malformed setup arguments before interactive auth", async () => {
    expect(hasValidSetupArguments(["setup", "--provider"])).toBe(false)
    expect(hasValidSetupArguments(["setup", "--unknown"])).toBe(false)
    expect(hasValidSetupArguments(["setup", "unexpected"])).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--provider"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--unknown"], {})).toBe(false)

    let authCalls = 0
    await expect(
      prepareCloudSession(["setup", "--provider"], {
        env: {},
        ensureCloudSession: async () => {
          authCalls += 1
          throw new Error("interactive auth must not run")
        }
      })
    ).rejects.toThrow(/--provider requires a value/)
    expect(authCalls).toBe(0)
  })

  it("rejects invalid setup values before interactive auth", async () => {
    expect(hasValidSetupArguments(["setup", "--connect-timeout=bogus"])).toBe(false)
    expect(hasValidSetupArguments(["setup", "--backend", "invalid"])).toBe(false)

    let authCalls = 0
    await expect(
      prepareCloudSession(["setup", "--backend", "invalid"], {
        env: {},
        ensureCloudSession: async () => {
          authCalls += 1
        }
      })
    ).rejects.toThrow(/unsupported integration backend/)
    expect(authCalls).toBe(0)
  })
})

describe("shouldPrepareCloudSession", () => {
  it("leaves help and caller-owned tokens alone", () => {
    expect(shouldPrepareCloudSession(["setup", "--help"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--help=true"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--help=false"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "-h=0"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--cloud-token", "explicit"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "-cloud-token", "explicit"], {})).toBe(false)
    expect(shouldPrepareCloudSession(["setup", "--cloud-token="], {})).toBe(true)
    expect(shouldPrepareCloudSession(["setup", "--cloud-token", ""], {})).toBe(true)
    expect(
      shouldPrepareCloudSession(["setup", "--cloud-token="], {
        RELAYFILE_CLOUD_TOKEN: "inherited-token"
      })
    ).toBe(true)
    expect(
      shouldPrepareCloudSession(["setup"], { RELAYFILE_CLOUD_TOKEN: "inherited-token" })
    ).toBe(false)
    expect(shouldPrepareCloudSession([], { CLOUD_API_ACCESS_TOKEN: "ci-token" })).toBe(false)
    expect(shouldPrepareCloudSession(["status"], {})).toBe(false)
  })

  it("accepts valid explicit setup arguments", () => {
    expect(
      hasValidSetupArguments([
        "setup",
        "--provider",
        "github",
        "--workspace=frontend",
        "--once",
        "--no-open=true"
      ])
    ).toBe(true)
    expect(
      shouldPrepareCloudSession(
        ["setup", "--provider", "github", "--workspace=frontend", "--once"],
        {}
      )
    ).toBe(true)
  })

  it("never prepares a session for a non-setup command", () => {
    for (const command of ["status", "mount", "workspace", "tree", "logs"]) {
      expect(shouldPrepareCloudSession([command], {})).toBe(false)
    }
  })
})

describe("parseGoDurationMilliseconds", () => {
  it("validates and converts Go durations for SDK login", () => {
    expect(parseGoDurationMilliseconds("10s")).toBe(10000)
    expect(parseGoDurationMilliseconds("1m30.5s")).toBe(90500)
    expect(parseGoDurationMilliseconds("250ms")).toBe(250)
    expect(parseGoDurationMilliseconds("0")).toBe(0)
    expect(parseGoDurationMilliseconds("bogus")).toBeNull()
    expect(parseGoDurationMilliseconds("10")).toBeNull()
    expect(parseGoDurationMilliseconds("")).toBeNull()
  })
})
