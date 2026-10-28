import { useState } from "react";
import {
  Autocomplete,
  Box,
  Checkbox,
  Collapse,
  FormControl,
  FormControlLabel,
  FormGroup,
  FormHelperText,
  FormLabel,
  Link,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { listCards } from "@/queries/cards";
import {
  type AccessTechnology,
  createJobs,
  type JobOperation,
  type JobParams,
} from "@/queries/jobs";
import {
  ACCESS_TECHNOLOGIES,
  ALL_ACCESS_TECHNOLOGIES,
  accessLabels,
  DEFAULT_ACCESS,
  isMCC,
  isMNC,
  isSlot,
  MAX_SLOT,
  MORE_ACCESS_TECHNOLOGIES,
  operationLabels,
} from "@/utils/jobs";

// CARD_OPTIONS is the number of cards offered: the first page, as many as a request takes.
const CARD_OPTIONS = 100;

const jobs = (n: number) => (n === 1 ? "1 job" : `${n} jobs`);

export default function NewJobDialog({
  iccids: initial = [],
  onClose,
}: {
  // iccids are the cards chosen to begin with.
  iccids?: string[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [iccids, setIccids] = useState<string[]>(initial);
  const [operation, setOperation] = useState<JobOperation>("oplmn_read");
  const [slot, setSlot] = useState("");
  const [mcc, setMcc] = useState("");
  const [mnc, setMnc] = useState("");
  const [access, setAccess] = useState<AccessTechnology[]>(DEFAULT_ACCESS);
  const [more, setMore] = useState(false);

  const cards = useQuery({
    queryKey: ["cards", { page: 1, pageSize: CARD_OPTIONS }],
    queryFn: () => listCards({ page: 1, perPage: CARD_OPTIONS }),
  });
  const byIccid = new Map((cards.data?.items ?? []).map((c) => [c.iccid, c]));
  // The cards chosen to begin with are offered even when they are not on the first page.
  const options = [
    ...initial.filter((i) => !byIccid.has(i)),
    ...byIccid.keys(),
  ];

  // params are the fields of the operation only: the server rejects the others.
  const params = (): JobParams => {
    switch (operation) {
      case "oplmn_set":
        return {
          iccids,
          operation,
          slot: Number(slot),
          mcc,
          mnc,
          access: ALL_ACCESS_TECHNOLOGIES.filter((a) => access.includes(a)),
        };
      case "oplmn_clear":
        return { iccids, operation, slot: Number(slot) };
      default:
        return { iccids, operation };
    }
  };

  const mutation = useMutation({
    mutationFn: () => createJobs(params()),
    onSuccess: (created) => {
      void queryClient.invalidateQueries({ queryKey: ["jobs"] });
      showSnackbar(`${jobs(created.length)} queued.`, "success");
      onClose();
    },
  });

  const toggle = (a: AccessTechnology) =>
    setAccess((as) =>
      as.includes(a) ? as.filter((o) => o !== a) : [...as, a],
    );

  const checkbox = (a: AccessTechnology) => (
    <FormControlLabel
      key={a}
      label={accessLabels[a]}
      control={
        <Checkbox checked={access.includes(a)} onChange={() => toggle(a)} />
      }
    />
  );

  const slotError =
    slot === "" || isSlot(slot) ? undefined : `1 to ${MAX_SLOT}.`;
  const mccError = mcc === "" || isMCC(mcc) ? undefined : "3 digits.";
  const mncError = mnc === "" || isMNC(mnc) ? undefined : "2 or 3 digits.";
  const accessError = access.length === 0 ? "Choose at least one." : undefined;

  const valid =
    iccids.length > 0 &&
    (operation === "oplmn_read" || isSlot(slot)) &&
    (operation !== "oplmn_set" ||
      (isMCC(mcc) && isMNC(mnc) && access.length > 0));

  return (
    <EditDialog
      title="New Job"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel="Create"
      pendingLabel="Creating…"
      failure="Could not create the jobs"
    >
      <Autocomplete
        multiple
        options={options}
        value={iccids}
        // A disabled option is only hidden from the pointer: a card without an MSISDN is kept out here too.
        onChange={(_e, v) =>
          setIccids(v.filter((i) => byIccid.get(i)?.msisdn !== ""))
        }
        loading={cards.isPending}
        getOptionDisabled={(iccid) => !byIccid.get(iccid)?.msisdn}
        renderOption={({ key, ...props }, iccid) => {
          const card = byIccid.get(iccid);
          const detail = [
            card?.msisdn || "no MSISDN",
            card?.description,
          ].filter(Boolean);
          return (
            <Box component="li" key={key} {...props}>
              <Stack>
                <span>{iccid}</span>
                <Typography variant="body2" color="text.secondary">
                  {detail.join(" · ")}
                </Typography>
              </Stack>
            </Box>
          );
        }}
        renderInput={(input) => (
          <TextField
            {...input}
            label="Cards"
            required={iccids.length === 0}
            error={cards.error !== null}
            helperText={
              cards.error
                ? `Could not load the cards: ${cards.error.message}`
                : "A card needs an MSISDN, and may have one job in progress."
            }
          />
        )}
      />
      <TextField
        select
        label="Operation"
        value={operation}
        onChange={(e) => setOperation(e.target.value as JobOperation)}
      >
        {(Object.keys(operationLabels) as JobOperation[]).map((o) => (
          <MenuItem key={o} value={o}>
            {operationLabels[o]}
          </MenuItem>
        ))}
      </TextField>
      {operation !== "oplmn_read" && (
        <TextField
          label="Slot"
          value={slot}
          onChange={(e) => setSlot(e.target.value.trim())}
          error={slotError !== undefined}
          helperText={slotError ?? "The entry of the roaming list, from 1."}
          inputMode="numeric"
          required
        />
      )}
      {operation === "oplmn_set" && (
        <>
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1}>
            <TextField
              label="MCC"
              value={mcc}
              onChange={(e) => setMcc(e.target.value.trim())}
              error={mccError !== undefined}
              helperText={mccError}
              inputMode="numeric"
              placeholder="999"
              required
              sx={{ flex: 1 }}
            />
            <TextField
              label="MNC"
              value={mnc}
              onChange={(e) => setMnc(e.target.value.trim())}
              error={mncError !== undefined}
              helperText={mncError}
              inputMode="numeric"
              placeholder="99"
              required
              sx={{ flex: 1 }}
            />
          </Stack>
          <FormControl error={accessError !== undefined}>
            <FormLabel>Access Technologies</FormLabel>
            <FormGroup row>{ACCESS_TECHNOLOGIES.map(checkbox)}</FormGroup>
            {accessError && <FormHelperText>{accessError}</FormHelperText>}
            {/* The others are rare in a private network: they are shown on demand, as an error's details. */}
            <Box>
              <Link
                component="button"
                type="button"
                variant="body2"
                underline="hover"
                aria-expanded={more}
                onClick={() => setMore((shown) => !shown)}
              >
                {more
                  ? "Fewer access technologies"
                  : "More access technologies"}
              </Link>
            </Box>
            <Collapse in={more}>
              <FormGroup row>
                {MORE_ACCESS_TECHNOLOGIES.map(checkbox)}
              </FormGroup>
            </Collapse>
          </FormControl>
        </>
      )}
    </EditDialog>
  );
}
