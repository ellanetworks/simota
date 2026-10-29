import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Divider,
  IconButton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import {
  Close as CloseIcon,
  DragIndicator as DragIndicatorIcon,
} from "@mui/icons-material";
import { useQuery } from "@tanstack/react-query";
import Fields from "@/components/Fields";
import QueryAlert from "@/components/QueryAlert";
import { JobStatusChip } from "@/components/chips";
import { getJob, type Job } from "@/queries/jobs";
import { formatTimestamp } from "@/utils/dates";
import {
  accessLabel,
  inProgress,
  jobLabel,
  plmnLabel,
  porDetail,
} from "@/utils/jobs";
import { TABLE_CONTAINER_SX } from "@/utils/layout";

export const DETAIL_REFRESH_MS = 5000;

// The panel is smsc's message panel: under the app bar, resizable between PANEL_MIN_WIDTH and PANEL_MAX_VW of the
// window.
const PANEL_DEFAULT_WIDTH = 560;
const PANEL_MIN_WIDTH = 350;
const PANEL_MAX_VW = 0.8;
const TOOLBAR_HEIGHT = 64;

const octets = (o: string): ReactNode => (
  <Box component="span" sx={{ fontFamily: "monospace" }}>
    {o === "" ? "—" : o}
  </Box>
);

// Entries are the entries a job read: the roaming list for a read, the slot read back for a set or a clear. An unused
// entry has no PLMN.
function Entries({ job }: { job: Job }) {
  const { entries } = job;
  const title =
    job.operation === "oplmn_read"
      ? `Roaming list${entries ? ` (${entries.length})` : ""}`
      : "Slot read back";

  return (
    <Box component="section" aria-labelledby="entries-title">
      <Typography
        id="entries-title"
        variant="subtitle1"
        component="h3"
        sx={{ mb: 1 }}
      >
        {title}
      </Typography>
      {!entries && <Typography color="text.secondary">Not read.</Typography>}
      {entries && (
        <TableContainer sx={TABLE_CONTAINER_SX}>
          <Table size="small" aria-labelledby="entries-title">
            <TableHead>
              <TableRow>
                <TableCell>Slot</TableCell>
                <TableCell>PLMN</TableCell>
                <TableCell>Access technologies</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {entries.map((e) => (
                <TableRow key={e.slot}>
                  <TableCell>{e.slot}</TableCell>
                  <TableCell>{plmnLabel(e.mcc, e.mnc)}</TableCell>
                  <TableCell>
                    {e.mcc ? accessLabel(e.access, e.access_raw) : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

// IMSIReadBack is the IMSI that an IMSI write read back: what the card holds, even when it is not the IMSI written.
function IMSIReadBack({ job }: { job: Job }) {
  return (
    <Box component="section" aria-labelledby="read-back-title">
      <Typography
        id="read-back-title"
        variant="subtitle1"
        component="h3"
        sx={{ mb: 1 }}
      >
        IMSI read back
      </Typography>
      <Typography
        color={job.imsi_read_back ? undefined : "text.secondary"}
        sx={job.imsi_read_back ? { fontFamily: "monospace" } : undefined}
      >
        {job.imsi_read_back ?? "Not read."}
      </Typography>
    </Box>
  );
}

// Packets shows the command sent to the card and its answer only when asked to, as smsc shows a message's content.
function Packets({ job }: { job: Job }) {
  const [shown, setShown] = useState(false);

  return (
    <Box component="section" aria-labelledby="packets-title">
      <Typography
        id="packets-title"
        variant="subtitle1"
        component="h3"
        sx={{ mb: 1 }}
      >
        Packets
      </Typography>
      {!shown && (
        <Button variant="outlined" onClick={() => setShown(true)}>
          Show packets
        </Button>
      )}
      {shown && (
        <Fields
          rows={[
            ["Command", octets(job.command ?? "")],
            ["Response", octets(job.response ?? "")],
          ]}
        />
      )}
    </Box>
  );
}

function JobDetail({
  initial,
  onClose,
}: {
  initial: Job;
  onClose: () => void;
}) {
  const query = useQuery({
    queryKey: ["job", initial.id],
    queryFn: () => getJob(initial.id),
    placeholderData: initial,
    refetchInterval: (q) =>
      inProgress(q.state.data ?? initial) ? DETAIL_REFRESH_MS : false,
  });

  const job = query.data ?? initial;
  const dash = (v?: string | number) => (v === undefined || v === "" ? "—" : v);

  const rows: [string, ReactNode][] = [
    ["Card", job.iccid],
    ["Operation", jobLabel(job)],
  ];
  if (job.operation === "oplmn_set") {
    rows.push(["Access technologies", accessLabel(job.access, job.access_raw)]);
  }
  if (job.operation === "imsi_write") {
    rows.push(["Replaced IMSI", dash(job.previous_imsi)]);
  }
  rows.push(
    ["TAR", dash(job.tar)],
    ["Key set", dash(job.kvn)],
    ["Created", formatTimestamp(job.created_at)],
    ["Updated", formatTimestamp(job.updated_at)],
    // The step counts the job's commands from 1.
    ["Step", job.step === undefined ? "—" : job.step + 1],
    ["Counter", dash(job.counter)],
  );
  if (job.operation === "oplmn_read") {
    rows.push(["File size", dash(job.file_size)]);
  }
  rows.push(
    ["SMSC message", dash(job.message_id)],
    ["PoR", porDetail(job.por_status)],
    [
      "Status word",
      job.status_word && job.warning ? (
        <>
          {job.status_word}{" "}
          <Box component="span" sx={{ color: "warning.main" }}>
            (warning)
          </Box>
        </>
      ) : (
        dash(job.status_word)
      ),
    ],
  );

  return (
    <>
      <Box
        sx={{
          px: 2,
          py: 1.5,
          borderBottom: 1,
          borderColor: "divider",
          display: "flex",
          alignItems: "center",
          gap: 1,
        }}
      >
        <Box
          sx={{
            flex: 1,
            minWidth: 0,
            display: "flex",
            alignItems: "center",
            gap: 1,
          }}
        >
          <Typography id="job-drawer-title" variant="h6" component="h2" noWrap>
            Job {job.id}
          </Typography>
          <JobStatusChip status={job.status} />
        </Box>
        <IconButton aria-label="Close" onClick={onClose} size="small">
          <CloseIcon />
        </IconButton>
      </Box>
      <Stack spacing={2} sx={{ p: 2, overflow: "auto", flex: 1 }}>
        <QueryAlert query={query} subject="job" />
        {(job.status === "failed" || job.status === "unconfirmed") &&
          job.error && (
            <Alert severity={job.status === "failed" ? "error" : "warning"}>
              {job.error}
            </Alert>
          )}
        <Fields rows={rows} />
        <Divider />
        {job.operation === "imsi_write" ? (
          <IMSIReadBack job={job} />
        ) : (
          <Entries job={job} />
        )}
        <Divider />
        <Packets job={job} />
      </Stack>
    </>
  );
}

interface JobDrawerProps {
  job: Job | null;
  onClose: () => void;
}

// JobDrawer shows a job beside the list, as smsc's MessageDrawer shows a message: the list stays usable, and Escape
// closes it.
export default function JobDrawer({ job, onClose }: JobDrawerProps) {
  const open = job !== null;
  const panelRef = useRef<HTMLElement>(null);
  const [width, setWidth] = useState(PANEL_DEFAULT_WIDTH);

  // Opening the panel moves focus into it, and closing it gives focus back, to the row that opened it.
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement;
    panelRef.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) {
        previous.focus();
      }
    };
  }, [open]);
  const [dragging, setDragging] = useState(false);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const onResizeMouseDown = (e: React.MouseEvent) => {
    e.preventDefault();
    setDragging(true);

    const onMouseMove = (ev: MouseEvent) => {
      const max = window.innerWidth * PANEL_MAX_VW;
      setWidth(
        Math.max(
          PANEL_MIN_WIDTH,
          Math.min(max, window.innerWidth - ev.clientX),
        ),
      );
    };
    const onMouseUp = () => {
      setDragging(false);
      document.removeEventListener("mousemove", onMouseMove);
      document.removeEventListener("mouseup", onMouseUp);
    };
    document.addEventListener("mousemove", onMouseMove);
    document.addEventListener("mouseup", onMouseUp);
  };

  return (
    <Box
      component="aside"
      ref={panelRef}
      tabIndex={-1}
      aria-labelledby={open ? "job-drawer-title" : undefined}
      aria-hidden={!open}
      inert={!open}
      sx={{
        position: "fixed",
        top: TOOLBAR_HEIGHT,
        right: 0,
        bottom: 0,
        width: { xs: "100%", sm: width },
        transform: open ? "translateX(0)" : "translateX(100%)",
        transition: dragging ? "none" : "transform 200ms ease-in-out",
        zIndex: (t) => t.zIndex.appBar - 1,
        bgcolor: "background.paper",
        boxShadow: open ? 8 : "none",
        display: "flex",
        flexDirection: "row",
        outline: "none",
      }}
    >
      <Box
        onMouseDown={onResizeMouseDown}
        sx={{
          width: 12,
          flexShrink: 0,
          cursor: "ew-resize",
          display: { xs: "none", sm: "flex" },
          alignItems: "center",
          justifyContent: "center",
          "&:hover .resizeIcon": { opacity: 1 },
        }}
      >
        <DragIndicatorIcon
          className="resizeIcon"
          sx={{
            fontSize: 20,
            opacity: 0.5,
            transition: "opacity 120ms",
            color: "text.secondary",
          }}
        />
      </Box>
      <Box
        sx={{
          flex: 1,
          minWidth: 0,
          display: "flex",
          flexDirection: "column",
          borderLeft: 1,
          borderColor: "divider",
        }}
      >
        {job && <JobDetail key={job.id} initial={job} onClose={onClose} />}
      </Box>
    </Box>
  );
}
