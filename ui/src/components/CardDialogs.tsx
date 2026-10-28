import { useState } from "react";
import {
  Box,
  MenuItem,
  Stack,
  TextField,
  type TextFieldProps,
  Typography,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import {
  type Card,
  type KeyAlgorithm,
  createCard,
  updateCard,
} from "@/queries/cards";
import {
  isICCID,
  isIMSI,
  isKey,
  isMSISDN,
  isTAR,
  keyHelp,
} from "@/utils/cards";

export const DEFAULT_TAR = "b00010";

export const algorithmLabel = (algorithm: KeyAlgorithm): string =>
  algorithm === "3des" ? "3DES" : "AES";

// errorFor is the error of a field, shown once something is typed: an empty required field only keeps the form
// from submitting.
const errorFor = (value: string, valid: boolean, message: string) =>
  value === "" || valid ? undefined : message;

const hex = (value: string) => value.replace(/\s/g, "");

// HexField is a key in hex. Keys are write-only: the server never returns them.
function HexField(props: TextFieldProps) {
  return (
    <TextField
      {...props}
      autoComplete="off"
      slotProps={{ htmlInput: { spellCheck: false } }}
    />
  );
}

// The fields a card's add and edit dialogs share.
interface Profile {
  imsi: string;
  msisdn: string;
  description: string;
  tar: string;
}

const profileErrors = (p: Profile) => ({
  imsi: errorFor(p.imsi, isIMSI(p.imsi), "6 to 15 digits."),
  msisdn: errorFor(
    p.msisdn,
    isMSISDN(p.msisdn),
    "An E.164 number, such as +15550000001.",
  ),
  tar: errorFor(p.tar, isTAR(p.tar), "6 hex characters, such as b00010."),
});

const profileValid = (p: Profile) =>
  isIMSI(p.imsi) && isMSISDN(p.msisdn) && isTAR(p.tar);

function ProfileFields({
  profile,
  onChange,
}: {
  profile: Profile;
  onChange: (change: Partial<Profile>) => void;
}) {
  const errors = profileErrors(profile);

  return (
    <>
      <TextField
        label="IMSI"
        value={profile.imsi}
        onChange={(e) => onChange({ imsi: e.target.value.trim() })}
        error={errors.imsi !== undefined}
        helperText={errors.imsi}
        inputMode="numeric"
        placeholder="001010000000001"
        required
      />
      <TextField
        label="MSISDN"
        value={profile.msisdn}
        onChange={(e) => onChange({ msisdn: e.target.value.trim() })}
        error={errors.msisdn !== undefined}
        helperText={
          errors.msisdn ?? "The number the platform sends OTA SMS to. Optional."
        }
        placeholder="+15550000001"
      />
      <TextField
        label="Description"
        value={profile.description}
        onChange={(e) => onChange({ description: e.target.value })}
      />
      <TextField
        label="TAR"
        value={profile.tar}
        onChange={(e) => onChange({ tar: e.target.value.trim() })}
        error={errors.tar !== undefined}
        helperText={
          errors.tar ??
          "The Toolkit Application Reference that commands go to, such as b00010 for SIM RFM."
        }
        required
      />
    </>
  );
}

export function AddCardDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [iccid, setIccid] = useState("");
  const [profile, setProfile] = useState<Profile>({
    imsi: "",
    msisdn: "",
    description: "",
    tar: DEFAULT_TAR,
  });
  const [version, setVersion] = useState("1");
  const [algorithm, setAlgorithm] = useState<KeyAlgorithm>("3des");
  const [kic, setKic] = useState("");
  const [kid, setKid] = useState("");
  const [kik, setKik] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      createCard({
        iccid,
        ...profile,
        kvn: Number(version),
        key_sets: [{ version: Number(version), algorithm, kic, kid, kik }],
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cards"] });
      showSnackbar("Card added successfully.", "success");
      onClose();
    },
  });

  const versionNumber = Number(version);
  // A key set version is the 4 bits of the KIc and KID that name it.
  const versionValid =
    /^\d+$/.test(version) && versionNumber >= 1 && versionNumber <= 15;
  const iccidError = errorFor(iccid, isICCID(iccid), "18 to 20 digits.");
  const keyError = (key: string) =>
    errorFor(key, isKey(key, algorithm), keyHelp(algorithm));

  const valid =
    isICCID(iccid) &&
    profileValid(profile) &&
    versionValid &&
    isKey(kic, algorithm) &&
    isKey(kid, algorithm) &&
    (kik === "" || isKey(kik, algorithm));

  return (
    <EditDialog
      title="Add Card"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel="Add"
      pendingLabel="Adding…"
      failure="Could not add the card"
    >
      <TextField
        label="ICCID"
        value={iccid}
        onChange={(e) => setIccid(e.target.value.trim())}
        error={iccidError !== undefined}
        helperText={iccidError}
        inputMode="numeric"
        placeholder="8949440000001659208"
        required
        autoFocus
      />
      <ProfileFields
        profile={profile}
        onChange={(change) => setProfile((p) => ({ ...p, ...change }))}
      />
      <Box>
        <Typography variant="subtitle2">Key Set</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
          The keys that secure commands to the card, in hex. They cannot be read
          back.
        </Typography>
        <Stack spacing={2}>
          <Stack direction={{ xs: "column", sm: "row" }} spacing={1}>
            <TextField
              label="Version"
              value={version}
              onChange={(e) => setVersion(e.target.value.trim())}
              error={!versionValid}
              helperText={versionValid ? undefined : "1 to 15."}
              inputMode="numeric"
              required
              sx={{ flex: 1 }}
            />
            <TextField
              select
              label="Algorithm"
              value={algorithm}
              onChange={(e) => setAlgorithm(e.target.value as KeyAlgorithm)}
              sx={{ flex: 1 }}
            >
              <MenuItem value="3des">3DES</MenuItem>
              <MenuItem value="aes">AES</MenuItem>
            </TextField>
          </Stack>
          <HexField
            label="KIc"
            value={kic}
            onChange={(e) => setKic(hex(e.target.value))}
            error={keyError(kic) !== undefined}
            helperText={keyError(kic) ?? "The ciphering key."}
            required
          />
          <HexField
            label="KID"
            value={kid}
            onChange={(e) => setKid(hex(e.target.value))}
            error={keyError(kid) !== undefined}
            helperText={keyError(kid) ?? "The cryptographic checksum key."}
            required
          />
          <HexField
            label="KIK"
            value={kik}
            onChange={(e) => setKik(hex(e.target.value))}
            error={keyError(kik) !== undefined}
            helperText={keyError(kik) ?? "The key that ciphers keys. Optional."}
          />
        </Stack>
      </Box>
    </EditDialog>
  );
}

export function EditCardDialog({
  card,
  onClose,
}: {
  card: Card;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [profile, setProfile] = useState<Profile>({
    imsi: card.imsi,
    msisdn: card.msisdn,
    description: card.description,
    tar: card.tar,
  });
  const [kvn, setKvn] = useState(card.kvn);

  const mutation = useMutation({
    mutationFn: () => updateCard(card.iccid, { ...profile, kvn }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cards"] });
      showSnackbar("Card updated successfully.", "success");
      onClose();
    },
  });

  return (
    <EditDialog
      title={`Edit Card ${card.iccid}`}
      valid={profileValid(profile)}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      failure="Could not update the card"
    >
      <ProfileFields
        profile={profile}
        onChange={(change) => setProfile((p) => ({ ...p, ...change }))}
      />
      <TextField
        select
        label="Key Set"
        value={kvn}
        onChange={(e) => setKvn(Number(e.target.value))}
        helperText="The key set that secures commands to the card."
      >
        {card.key_sets.map((k) => (
          <MenuItem key={k.version} value={k.version}>
            {`${k.version} (${algorithmLabel(k.algorithm)})`}
          </MenuItem>
        ))}
      </TextField>
    </EditDialog>
  );
}
