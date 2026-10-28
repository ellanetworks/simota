import { useState } from "react";
import { Box, Button, MenuItem, TextField, Typography } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { CCLengthField } from "@/components/CardDialogs";
import { type CCLength, importCards, type KeyAlgorithm } from "@/queries/cards";

const cards = (n: number) => (n === 1 ? "1 card" : `${n} cards`);

export default function ImportCardsDialog({
  onClose,
}: {
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [file, setFile] = useState<File | null>(null);
  // A key file does not tell its key sets' algorithm: it is chosen, with no default.
  const [algorithm, setAlgorithm] = useState<KeyAlgorithm | "">("");
  const [ccLength, setCcLength] = useState<CCLength>(8);

  const mutation = useMutation({
    mutationFn: async () => {
      const text = await file!.text();
      // An empty file would import nothing: say so, rather than "Imported 0 cards".
      if (text.trim() === "") throw new Error(`${file!.name} is empty.`);
      return importCards(text, {
        algorithm: algorithm as KeyAlgorithm,
        ...(algorithm === "aes" && { ccLength }),
      });
    },
    onSuccess: ({ created, updated }) => {
      void queryClient.invalidateQueries({ queryKey: ["cards"] });
      showSnackbar(
        `Imported ${cards(created + updated)}: ${created} created, ${updated} updated`,
        "success",
      );
      onClose();
    },
  });

  return (
    <EditDialog
      title="Import Cards"
      valid={file !== null && algorithm !== ""}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel="Import"
      pendingLabel="Importing…"
      failure="Could not import the cards"
    >
      <Typography variant="body2" color="text.secondary">
        Card key file from the vendor, such as sysmocom&apos;s, with a header
        row. Imports ICCID, IMSI and the KIC/KID key sets; ignores Ki, OPc,
        PINs, ADM keys and KIKs.
      </Typography>
      <Box sx={{ display: "flex", alignItems: "center", gap: 2 }}>
        <Button component="label" variant="outlined" sx={{ flexShrink: 0 }}>
          Choose File
          <input
            type="file"
            accept=".csv,.tsv,.txt"
            hidden
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </Button>
        <Typography variant="body2" noWrap>
          {file?.name ?? "No file chosen."}
        </Typography>
      </Box>
      <TextField
        select
        label="Algorithm"
        value={algorithm}
        onChange={(e) => setAlgorithm(e.target.value as KeyAlgorithm)}
        helperText="The algorithm of the file's key sets."
        required
      >
        <MenuItem value="3des">3DES</MenuItem>
        <MenuItem value="aes">AES</MenuItem>
      </TextField>
      {algorithm === "aes" && (
        <CCLengthField value={ccLength} onChange={setCcLength} />
      )}
    </EditDialog>
  );
}
