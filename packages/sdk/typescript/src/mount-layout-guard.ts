import { MountSessionInputError } from "./setup-errors.js"

/** Refuse scoped runtime state until all operator surfaces can enumerate it. */
export function assertExactMountLayout(
  env: Record<string, string | undefined>
): void {
  if ((env.RELAYFILE_MOUNT_LOCAL_LAYOUT ?? "").trim().toLowerCase() !== "scoped") {
    return
  }
  throw new MountSessionInputError(
    "The TypeScript mount launcher does not support RELAYFILE_MOUNT_LOCAL_LAYOUT=scoped until scoped operator surfaces are ready; use RELAYFILE_MOUNT_LOCAL_LAYOUT=exact."
  )
}
