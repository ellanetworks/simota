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
  type CCLength,
  type KeyAlgorithm,
  type KeySet,
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

// DEFAULT_TAR is the first ADF RFM application linked to the USIM.
export const DEFAULT_TAR = "b00001";

export const algorithmLabel = (algorithm: KeyAlgorithm): string =>
  algorithm === "3des" ? "3DES" : "AES";

// keySetLabel is a key set, such as "1 (3DES, counter 3)", with the length of its checksums when AES may shorten it.
export const keySetLabel = (k: KeySet): string =>
  `${k.version} (${algorithmLabel(k.algorithm)}` +
  (k.algorithm === "aes" ? `, CC ${k.cc_length}` : "") +
  `, counter ${k.counter})`;

// CCLengthField chooses the length of an AES key set's cryptographic checksums.
export function CCLengthField({
  value,
  onChange,
}: {
  value: CCLength;
  onChange: (value: CCLength) => void;
}) {
  return (
    <TextField
      select
      label="CC Length"
      value={value}
      onChange={(e) => onChange(Number(e.target.value) as CCLength)}
      helperText="The length of the cryptographic checksums, in bytes."
    >
      <MenuItem value={8}>8</MenuItem>
      <MenuItem value={4}>4</MenuItem>
    </TextField>
  );
}

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
  tar: errorFor(
    p.tar,
    isTAR(p.tar),
    "b00001, b00010 to b0001f, or b00020 to b0011f.",
  ),
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
        helperText={
          errors.imsi ??
          "Optional: the IMSI the card holds. An IMSI write needs it."
        }
        inputMode="numeric"
        placeholder="001010000000001"
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
          "RFM TAR that reaches ADF.USIM (TS 101 220), e.g. b00001; sysmoISIM: b00011"
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
  const [ccLength, setCcLength] = useState<CCLength>(8);

  const mutation = useMutation({
    mutationFn: () =>
      createCard({
        iccid,
        ...profile,
        kvn: Number(version),
        key_sets: [
          {
            version: Number(version),
            algorithm,
            // A 3DES key set's checksums are 8 bytes, the server's default.
            ...(algorithm === "aes" && { cc_length: ccLength }),
            kic,
            kid,
          },
        ],
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
  const iccidError = errorFor(
    iccid,
    isICCID(iccid),
    "18 to 20 digits, and an optional trailing F.",
  );
  const keyError = (key: string) =>
    errorFor(key, isKey(key, algorithm), keyHelp(algorithm));

  const valid =
    isICCID(iccid) &&
    profileValid(profile) &&
    versionValid &&
    isKey(kic, algorithm) &&
    isKey(kid, algorithm);

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
          {algorithm === "aes" && (
            <CCLengthField value={ccLength} onChange={setCcLength} />
          )}
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
    imsi: card.imsi ?? "",
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
      title="Edit Card"
      valid={profileValid(profile)}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      failure="Could not update the card"
    >
      <TextField label="ICCID" value={card.iccid} disabled />
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
            {keySetLabel(k)}
          </MenuItem>
        ))}
      </TextField>
    </EditDialog>
  );
}
