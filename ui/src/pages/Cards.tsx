import { useState } from "react";
import {
  Box,
  Button,
  IconButton,
  Skeleton,
  Stack,
  Tooltip,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
  UploadFile as UploadFileIcon,
} from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AddCardDialog, EditCardDialog } from "@/components/CardDialogs";
import ConfirmDialog from "@/components/ConfirmDialog";
import ImportCardsDialog from "@/components/ImportCardsDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { type Card, deleteCard, listCards } from "@/queries/cards";
import { formatTimestamp } from "@/utils/dates";
import { GRID_SX } from "@/utils/layout";

const dash = (value: string) => value || "—";

const columns = (
  onEdit: (card: Card) => void,
  onDelete: (card: Card) => void,
): GridColDef<Card>[] => [
  { field: "iccid", headerName: "ICCID", flex: 1, minWidth: 200 },
  { field: "imsi", headerName: "IMSI", flex: 0.8, minWidth: 160 },
  {
    field: "msisdn",
    headerName: "MSISDN",
    flex: 0.7,
    minWidth: 140,
    valueGetter: (_value, row) => dash(row.msisdn),
  },
  {
    field: "description",
    headerName: "Description",
    flex: 1,
    minWidth: 140,
    valueGetter: (_value, row) => dash(row.description),
  },
  { field: "tar", headerName: "TAR", width: 90 },
  { field: "kvn", headerName: "Key set", width: 90 },
  {
    field: "key_sets",
    headerName: "Key sets",
    width: 100,
    sortable: false,
    valueGetter: (_value, row) =>
      dash(row.key_sets.map((k) => k.version).join(", ")),
  },
  {
    field: "updated_at",
    headerName: "Updated",
    width: 170,
    valueFormatter: (value: string) => formatTimestamp(value),
  },
  {
    field: "actions",
    headerName: "Actions",
    width: 100,
    align: "right",
    headerAlign: "right",
    sortable: false,
    renderCell: ({ row }) => (
      <>
        <Tooltip title="Edit card" arrow>
          <IconButton
            size="small"
            aria-label={`Edit ${row.iccid}`}
            onClick={() => onEdit(row)}
          >
            <EditIcon fontSize="small" color="primary" />
          </IconButton>
        </Tooltip>
        <Tooltip title="Delete card" arrow>
          <IconButton
            size="small"
            aria-label={`Delete ${row.iccid}`}
            onClick={() => onDelete(row)}
          >
            <DeleteIcon fontSize="small" color="error" />
          </IconButton>
        </Tooltip>
      </>
    ),
  },
];

export default function Cards() {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [adding, setAdding] = useState(false);
  const [importing, setImporting] = useState(false);
  const [editing, setEditing] = useState<Card | null>(null);
  const [deleting, setDeleting] = useState<Card | null>(null);

  const cards = useQuery({ queryKey: ["cards"], queryFn: listCards });

  const remove = useMutation({
    mutationFn: (card: Card) => deleteCard(card.iccid),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cards"] });
      showSnackbar("Card deleted successfully.", "success");
      setDeleting(null);
    },
    onError: (error) => {
      setDeleting(null);
      showSnackbar(`Failed to delete card: ${errorMessage(error)}`, "error");
    },
  });

  return (
    <Box component="section" aria-labelledby="cards-title">
      <PageHeader
        id="cards-title"
        title="Cards"
        count={cards.data?.length}
        description="The SIM cards this platform manages over the air, and their OTA keys."
        action={
          <Stack direction="row" spacing={1} sx={{ flexShrink: 0 }}>
            <Button
              variant="outlined"
              startIcon={<UploadFileIcon />}
              onClick={() => setImporting(true)}
            >
              Import
            </Button>
            <Button
              variant="contained"
              color="success"
              startIcon={<AddIcon />}
              onClick={() => setAdding(true)}
            >
              Add
            </Button>
          </Stack>
        }
      />
      <QueryAlert query={cards} subject="cards" />
      {cards.isPending && <Skeleton variant="rounded" height={160} />}
      {cards.data && (
        <DataGrid<Card>
          aria-label="Cards"
          rows={cards.data}
          columns={columns(setEditing, setDeleting)}
          getRowId={(card) => card.iccid}
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          pageSizeOptions={[25, 50, 100]}
          initialState={{ pagination: { paginationModel: { pageSize: 25 } } }}
          localeText={{
            noRowsLabel: "No cards. Add one, or import a key file.",
          }}
          autoHeight
          sx={GRID_SX}
        />
      )}
      {adding && <AddCardDialog onClose={() => setAdding(false)} />}
      {importing && <ImportCardsDialog onClose={() => setImporting(false)} />}
      {editing && (
        <EditCardDialog card={editing} onClose={() => setEditing(null)} />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete Card"
          description={`Delete ${deleting.iccid}? Its keys are deleted with it, and the platform can no longer reach the card.`}
          confirmLabel="Delete"
          pending={remove.isPending}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </Box>
  );
}
