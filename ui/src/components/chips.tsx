import { Chip, type ChipProps, Tooltip } from "@mui/material";
import type { JobStatus } from "@/queries/jobs";
import type { BindStatus } from "@/queries/smsc";
import { statusHelp } from "@/utils/jobs";

const statusColors: Record<JobStatus, ChipProps["color"]> = {
  queued: "info",
  sent: "info",
  succeeded: "success",
  // An unconfirmed write may have taken effect.
  unconfirmed: "warning",
  failed: "error",
};

// JobStatusChip is a job's status, with what it means on hover, as smsc's peer state chip.
export function JobStatusChip({ status }: { status: JobStatus }) {
  return (
    <Tooltip title={statusHelp[status]} arrow>
      <Chip label={status} color={statusColors[status]} size="small" />
    </Tooltip>
  );
}

const bindColors: Record<BindStatus, ChipProps["color"]> = {
  unset: "default",
  connecting: "info",
  bound: "success",
  disconnected: "error",
};

const bindHelp: Record<BindStatus, string> = {
  unset: "No SMSC is set.",
  connecting: "simota is connecting to the SMSC and binding.",
  bound: "simota is bound to the SMSC as a transceiver.",
  disconnected: "The session failed; simota binds again shortly.",
};

// BindStatusChip is the state of simota's SMPP session to the SMSC, with what it means on hover.
export function BindStatusChip({ status }: { status: BindStatus }) {
  return (
    <Tooltip title={bindHelp[status]} arrow>
      <Chip label={status} color={bindColors[status]} size="small" />
    </Tooltip>
  );
}
