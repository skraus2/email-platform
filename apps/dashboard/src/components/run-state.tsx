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
export function RunPosition({ run }: { run: AutomationRun }) {
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
  return <>Ended at step {run.step + 1}</>;
}
