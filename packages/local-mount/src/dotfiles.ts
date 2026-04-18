import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';

export interface ReadAgentDotfilesOptions {
  /**
   * If provided, also reads `.{agentName}.agentignore` and `.{agentName}.agentreadonly`
   * from the project directory and merges the resulting patterns.
   */
  agentName?: string;
}

export interface AgentDotfilePatterns {
  ignoredPatterns: string[];
  readonlyPatterns: string[];
}

function cleanPatterns(content: string): string[] {
  return content
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line !== '' && !line.startsWith('#'));
}

function loadPatternsFromFile(filePath: string): string[] {
  if (!existsSync(filePath)) {
    return [];
  }
  const content = readFileSync(filePath, 'utf8');
  return cleanPatterns(content);
}

/**
 * Read `.agentignore` and `.agentreadonly` dotfiles from a project directory
 * and return the compiled pattern lists. If `agentName` is supplied, also
 * reads `.{agentName}.agentignore` and `.{agentName}.agentreadonly` and
 * appends their patterns.
 */
export function readAgentDotfiles(
  projectDir: string,
  options: ReadAgentDotfilesOptions = {}
): AgentDotfilePatterns {
  const resolvedProjectDir = path.resolve(projectDir);
  const ignoredPatterns: string[] = [
    ...loadPatternsFromFile(path.join(resolvedProjectDir, '.agentignore')),
  ];
  const readonlyPatterns: string[] = [
    ...loadPatternsFromFile(path.join(resolvedProjectDir, '.agentreadonly')),
  ];

  if (options.agentName) {
    const safeAgentName = sanitizeAgentName(options.agentName);
    ignoredPatterns.push(
      ...loadPatternsFromFile(path.join(resolvedProjectDir, `.${safeAgentName}.agentignore`))
    );
    readonlyPatterns.push(
      ...loadPatternsFromFile(path.join(resolvedProjectDir, `.${safeAgentName}.agentreadonly`))
    );
  }

  return { ignoredPatterns, readonlyPatterns };
}

const AGENT_NAME_PATTERN = /^[A-Za-z0-9_-]+$/;

function sanitizeAgentName(agentName: string): string {
  if (!AGENT_NAME_PATTERN.test(agentName) || path.basename(agentName) !== agentName) {
    throw new Error(
      `Invalid agentName ${JSON.stringify(agentName)}: only [A-Za-z0-9_-] are allowed`
    );
  }
  return agentName;
}
