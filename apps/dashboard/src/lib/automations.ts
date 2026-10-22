import type { AutomationStep, RunStatus } from "./admin-api.ts";

/** Run statuses in the order they are shown. */
export const RUN_STATUSES: readonly RunStatus[] = [
  "active",
  "completed",
  "exited",
  "canceled",
  "failed",
];

/** A wait as text: "1 hour", "3 days 2 hours", "90 seconds". */
export function formatWait(seconds: number): string {
  const units: [number, string][] = [
    [86400, "day"],
    [3600, "hour"],
    [60, "minute"],
    [1, "second"],
  ];
  const parts: string[] = [];
  let rest = seconds;
  for (const [size, name] of units) {
    const n = Math.floor(rest / size);
    if (n > 0) {
      parts.push(`${n} ${name}${n === 1 ? "" : "s"}`);
      rest -= n * size;
    }
    if (parts.length === 2) {
      break;
    }
  }
  return parts.length === 0 ? "0 seconds" : parts.join(" ");
}

const OPS: Record<string, string> = {
  eq: "is",
  neq: "is not",
  exists: "is set",
  not_exists: "is not set",
  occurred: "was reported",
  not_occurred: "was not reported",
};

/** One step as a sentence for operators. */
export function describeStep(step: AutomationStep): string {
  switch (step.type) {
    case "wait":
      return `Wait ${formatWait(step.seconds)}`;
    case "condition": {
      const op = OPS[step.op] ?? step.op;
      if (step.event !== undefined) {
        return `Continue only if event "${step.event}" ${op} since the run started`;
      }
      const value =
        step.value === undefined ? "" : ` ${JSON.stringify(step.value)}`;
      return `Continue only if attribute "${step.attribute}" ${op}${value}`;
    }
    case "send":
      return `Send version ${step.template_version} from ${step.from}`;
  }
}
