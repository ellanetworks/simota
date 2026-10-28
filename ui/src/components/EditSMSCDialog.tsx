import { useState } from "react";
import { Checkbox, FormControlLabel, TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import PasswordField from "@/components/PasswordField";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { type SMSC, updateSMSC } from "@/queries/smsc";
import { isE164 } from "@/utils/e164";
import {
  isHost,
  isPassword,
  isSystemID,
  MAX_PASSWORD,
  MAX_SYSTEM_ID,
  MAX_VALIDITY_SECONDS,
  portOf,
  validityHours,
  validitySeconds,
} from "@/utils/smsc";

const MAX_VALIDITY_HOURS = MAX_VALIDITY_SECONDS / 3600;

export default function EditSMSCDialog({
  smsc,
  onClose,
}: {
  smsc: SMSC;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [host, setHost] = useState(smsc.host);
  const [port, setPort] = useState(String(smsc.port));
  const [tls, setTLS] = useState(smsc.tls);
  const [systemID, setSystemID] = useState(smsc.system_id);
  const [password, setPassword] = useState("");
  const [originator, setOriginator] = useState(smsc.originator);
  const [validity, setValidity] = useState(
    validityHours(smsc.validity_seconds),
  );

  const mutation = useMutation({
    mutationFn: () =>
      updateSMSC({
        host,
        port: portOf(port)!,
        tls,
        system_id: systemID,
        // An empty password keeps the one set: it is sent only when typed.
        ...(password !== "" && { password }),
        originator,
        validity_seconds: validitySeconds(validity)!,
      }),
    onSuccess: (updated) => {
      queryClient.setQueryData(["smsc"], updated);
      showSnackbar("SMSC updated successfully.", "success");
      onClose();
    },
  });

  const hostError =
    host === "" || isHost(host)
      ? undefined
      : "A host name or an IP address, without a port.";
  const portError =
    portOf(port) === undefined ? "A port from 1 to 65535." : undefined;
  const systemIDError =
    systemID === "" || isSystemID(systemID)
      ? undefined
      : `1 to ${MAX_SYSTEM_ID} printable ASCII characters.`;
  const passwordError =
    password === "" || isPassword(password)
      ? undefined
      : `1 to ${MAX_PASSWORD} printable ASCII characters.`;
  const originatorError =
    originator === "" || isE164(originator)
      ? undefined
      : "E.164, e.g. +15550000000";
  const validityError =
    validitySeconds(validity) === undefined
      ? `1 second to ${MAX_VALIDITY_HOURS} hours (63 weeks).`
      : undefined;

  const valid =
    isHost(host) &&
    portError === undefined &&
    isSystemID(systemID) &&
    (password === "" ? smsc.password_set : isPassword(password)) &&
    isE164(originator) &&
    validityError === undefined;

  return (
    <EditDialog
      title="Edit SMSC"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      failure="Could not update the SMSC"
    >
      <TextField
        label="Host"
        value={host}
        onChange={(e) => setHost(e.target.value.trim())}
        error={hostError !== undefined}
        helperText={hostError ?? "The host of Ella SMSC's SMPP listener."}
        placeholder="127.0.0.1"
        required
        autoFocus
      />
      <TextField
        label="Port"
        value={port}
        onChange={(e) => setPort(e.target.value.trim())}
        error={portError !== undefined}
        helperText={portError ?? "The SMPP port, 2775 by default."}
        inputMode="numeric"
        required
      />
      <FormControlLabel
        control={
          <Checkbox checked={tls} onChange={(e) => setTLS(e.target.checked)} />
        }
        label="TLS"
      />
      <TextField
        label="System ID"
        value={systemID}
        onChange={(e) => setSystemID(e.target.value.trim())}
        error={systemIDError !== undefined}
        helperText={
          systemIDError ??
          "The system_id of simota's ESME account on Ella SMSC."
        }
        required
      />
      <PasswordField
        label="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={passwordError !== undefined}
        helperText={
          passwordError ??
          (smsc.password_set
            ? "Leave empty to keep the current password"
            : "The password of the ESME account.")
        }
        required={!smsc.password_set}
        autoComplete="off"
      />
      <TextField
        label="Originator"
        value={originator}
        onChange={(e) => setOriginator(e.target.value.trim())}
        error={originatorError !== undefined}
        helperText={originatorError ?? "The number the OTA messages come from."}
        placeholder="+15550000000"
        required
      />
      <TextField
        label="Validity (hours)"
        value={validity}
        onChange={(e) => setValidity(e.target.value)}
        error={validityError !== undefined}
        helperText={
          validityError ?? "How long the SMSC attempts delivery of a command."
        }
        inputMode="decimal"
        required
      />
    </EditDialog>
  );
}
