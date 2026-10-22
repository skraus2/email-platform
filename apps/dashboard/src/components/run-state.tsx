import { Status } from "@/components/status";
import { Timestamp } from "@/components/timestamp";
import type { AutomationRun } from "@/lib/admin-api";
import { describeReason } from "@/lib/automations";

/** A run's status with why it ended (when that adds anything). */
export function RunStatus({ run }: { run: AutomationRun }) {
  return (
    <>
      <Status value={run.status} />
      {run.reason && run.reason !== run.status ? (
        <span className="muted"> ({describeReason(run.reason)})</span>
      ) : null}
    </>
  );
}

/**
 * Where a run stands: the next step and when it is due, or the step it
 * ended at. Steps are numbered from 1 as in the step list.
 */
export function RunPosition({
  run,
  steps,
}: {
  run: AutomationRun;
  /** Number of steps of the automation, when known. */
  steps?: number;
}) {
  if (run.status === "active" && steps !== undefined && run.step >= steps) {
    // A wait as the last step: nothing runs after it.
    return (
      <>
        Finishes <Timestamp iso={run.next_step_at} />
      </>
    );
  }
  if (run.status === "active") {
    return (
      <>
        Step {run.step + 1}, due <Timestamp iso={run.next_step_at} />
      </>
    );
  }
  if (run.status === "completed") {
    return <span className="muted">all steps done</span>;
  }
  // Cancelled runs and runs of a disabled project stopped before their
  // next step ran.
  if (run.status === "canceled" || run.reason === "project_disabled") {
    return <>Ended before step {run.step + 1}</>;
  }
  return <>Ended at step {run.step + 1}</>;
}
