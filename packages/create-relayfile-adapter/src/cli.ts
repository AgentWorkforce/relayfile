import path from "node:path";
import { scaffoldProject, type TemplateKind } from "./index.js";

interface ParsedArgs {
  help: boolean;
  name?: string;
  targetDirectory?: string;
  template: TemplateKind;
}

function printHelp(): void {
  console.log(`Usage: create-relayfile-adapter <name> [target-directory] [--template adapter|provider]

Options:
  --template, -t  Starter template to scaffold. Defaults to "adapter".
  --help, -h      Show this help message.
`);
}

function parseArgs(argv: readonly string[]): ParsedArgs {
  let help = false;
  let template: TemplateKind = "adapter";
  const positionals: string[] = [];

  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];

    if (argument === "--help" || argument === "-h") {
      help = true;
      continue;
    }

    if (argument === "--template" || argument === "-t") {
      const templateValue = argv[index + 1];
      if (!templateValue || (templateValue !== "adapter" && templateValue !== "provider")) {
        throw new Error('Expected "--template" to be followed by "adapter" or "provider".');
      }
      template = templateValue;
      index += 1;
      continue;
    }

    positionals.push(argument);
  }

  return {
    help,
    name: positionals[0],
    targetDirectory: positionals[1],
    template
  };
}

export async function run(argv: readonly string[]): Promise<void> {
  const parsed = parseArgs(argv);

  if (parsed.help || !parsed.name) {
    printHelp();
    return;
  }

  const result = await scaffoldProject({
    name: parsed.name,
    targetDirectory: parsed.targetDirectory,
    template: parsed.template
  });

  const relativeDestination = path.relative(process.cwd(), result.destination) || ".";
  console.log(`Created ${result.template} starter in ${relativeDestination}`);
  for (const file of result.files) {
    console.log(`- ${path.relative(process.cwd(), file)}`);
  }
}
