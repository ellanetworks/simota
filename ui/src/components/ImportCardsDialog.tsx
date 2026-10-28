import { useState } from "react";
import { Box, Button, Typography } from "@mui/material";
import { UploadFile as UploadFileIcon } from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { importCards } from "@/queries/cards";

const cards = (n: number) => (n === 1 ? "1 card" : `${n} cards`);

export default function ImportCardsDialog({
  onClose,
}: {
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [file, setFile] = useState<File | null>(null);

  const mutation = useMutation({
    mutationFn: async () => {
      const text = await file!.text();
      // An empty file would import nothing: say so, rather than "Imported 0 cards".
      if (text.trim() === "") throw new Error(`${file!.name} is empty.`);
      return importCards(text);
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
      valid={file !== null}
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
        row. Imports ICCID, IMSI and the KIC/KID/KIK key sets; ignores Ki, OPc,
        PINs and ADM keys.
      </Typography>
      <Box sx={{ display: "flex", alignItems: "center", gap: 2 }}>
        <Button
          component="label"
          variant="outlined"
          startIcon={<UploadFileIcon />}
          sx={{ flexShrink: 0 }}
        >
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
    </EditDialog>
  );
}
