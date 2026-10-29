import { useState } from "react";
import { Box, Button, Stack, Typography } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import { BindStatusChip } from "@/components/chips";
import EditSMSCDialog from "@/components/EditSMSCDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable from "@/components/SettingsTable";
import { BIND_REFRESH_MS, type BindState, smscQuery } from "@/queries/smsc";
import { formatDateTime } from "@/utils/dates";
import { formatDuration } from "@/utils/durations";

const orNotSet = (value: string) => value || "Not set";

// Bind is the bind state: a chip, and since when.
function Bind({ bind }: { bind: BindState }) {
  return (
    <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
      <BindStatusChip status={bind.status} />
      {bind.status !== "unset" && bind.since && (
        <Typography variant="body2" color="text.secondary">
          since {formatDateTime(bind.since)}
        </Typography>
      )}
    </Stack>
  );
}

// lastError is the last failure of the session, with its time, or "None".
const lastError = (bind: BindState) =>
  bind.last_error
    ? `${bind.last_error}${bind.last_error_at ? ` (${formatDateTime(bind.last_error_at)})` : ""}`
    : "None";

export default function SMSC() {
  const [editing, setEditing] = useState(false);
  // The bind state changes on its own: the page reads it again.
  const smsc = useQuery({ ...smscQuery, refetchInterval: BIND_REFRESH_MS });
  const { data, isPending } = smsc;

  return (
    <Box component="section" aria-labelledby="smsc-title">
      <PageHeader
        id="smsc-title"
        title="SMSC"
        description="The SMSC, such as Ella SMSC, that sends the commands to the cards over SMPP, and pushes back their answers."
        action={
          // The settings are set together, in one dialog.
          <Button
            variant="contained"
            color="success"
            onClick={() => setEditing(true)}
            disabled={!data}
          >
            Edit
          </Button>
        }
      />
      <QueryAlert query={smsc} subject="SMSC settings" />
      <SettingsTable
        label="SMSC settings"
        loading={isPending}
        rows={[
          {
            label: "Host",
            help: "The host of the SMSC's SMPP listener.",
            value: data && orNotSet(data.host),
          },
          {
            label: "Port",
            help: "The SMPP port.",
            value: data && String(data.port),
          },
          {
            label: "TLS",
            help: "Whether the SMPP connection is over TLS.",
            value: data && (data.tls ? "On" : "Off"),
          },
          {
            label: "System ID",
            help: "The system_id of simota's ESME account on the SMSC.",
            value: data && orNotSet(data.system_id),
          },
          {
            label: "Password",
            help: "The password of the ESME account. It cannot be read back.",
            value: data && (data.password_set ? "Set" : "Not set"),
          },
          {
            label: "Originator",
            help: "The E.164 number the OTA messages come from.",
            value: data && orNotSet(data.originator),
          },
          {
            label: "Validity",
            help: "How long the SMSC attempts delivery of a command.",
            value: data && formatDuration(data.validity_seconds),
          },
          {
            label: "Bind",
            help: "simota's SMPP session to the SMSC: one transceiver, bound again whenever it is lost.",
            value: data && <Bind bind={data.bind} />,
          },
          {
            label: "Last error",
            help: "The last failure to connect, to bind, or of the session.",
            value: data && lastError(data.bind),
          },
        ]}
      />
      {editing && data && (
        <EditSMSCDialog smsc={data} onClose={() => setEditing(false)} />
      )}
    </Box>
  );
}
