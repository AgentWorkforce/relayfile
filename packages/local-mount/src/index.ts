export {
  createMount,
  attachMount,
  type MountOptions,
  type MountHandle,
} from './mount.js';

export {
  type AutoSyncOptions,
  type AutoSyncHandle,
  type FileState,
} from './auto-sync.js';

export {
  readAgentDotfiles,
  type ReadAgentDotfilesOptions,
  type AgentDotfilePatterns,
} from './dotfiles.js';

export {
  launchOnMount,
  type LaunchOnMountOptions,
  type LaunchOnMountResult,
} from './launch.js';
