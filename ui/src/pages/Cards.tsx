import { useState } from "react";
import { Box, Button, IconButton, Stack, Tooltip } from "@mui/material";
import {
  Delete as DeleteIcon,
  Edit as EditIcon,
  PlaylistPlay as NewJobIcon,
} from "@mui/icons-material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AddCardDialog,
  EditCardDialog,
  keySetLabel,
} from "@/components/CardDialogs";
import ConfirmDialog from "@/components/ConfirmDialog";
import ImportCardsDialog from "@/components/ImportCardsDialog";
import NewJobDialog from "@/components/NewJobDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { type Card, deleteCard, listCards } from "@/queries/cards";
import { smscQuery } from "@/queries/smsc";
import { formatTimestamp } from "@/utils/dates";
import { GRID_SX } from "@/utils/layout";

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const dash = (value: string) => value || "—";

// hiddenByDefault are the columns the column menu shows on demand, as smsc's Messages page does, so that the grid
// fits the page.
const hiddenByDefault = {
  imsi: false,
  updated_at: false,
};

// newJobBlock is why a job cannot be created for a card, if it cannot.
const newJobBlock = (card: Card, smscUnset: boolean): string | undefined => {
  if (smscUnset) return "Set the SMSC first";
  // A job reaches a card by SMS, to its MSISDN.
  if (!card.msisdn) return "No MSISDN";
  return undefined;
};

const columns = (
  smscUnset: boolean,
  onNewJob: (card: Card) => void,
  onEdit: (card: Card) => void,
  onDelete: (card: Card) => void,
): GridColDef<Card>[] => [
  { field: "iccid", headerName: "ICCID", flex: 1, minWidth: 180 },
  {
    field: "imsi",
    headerName: "IMSI",
    flex: 0.8,
    minWidth: 150,
    valueGetter: (_value, row) => dash(row.imsi ?? ""),
  },
  {
    field: "msisdn",
    headerName: "MSISDN",
    flex: 0.7,
    minWidth: 130,
    valueGetter: (_value, row) => dash(row.msisdn),
  },
  {
    field: "description",
    headerName: "Description",
    flex: 1,
    minWidth: 110,
    valueGetter: (_value, row) => dash(row.description),
  },
  { field: "tar", headerName: "TAR", width: 80 },
  {
    field: "key_sets",
    headerName: "Key sets",
    flex: 1,
    minWidth: 190,
    // A key set to a line, as smsc lists a peer's routes.
    renderCell: ({ row }) =>
      row.key_sets.length === 0 ? (
        "—"
      ) : (
        <Stack>
          {row.key_sets.map((k) => (
            <span key={k.version}>
              {keySetLabel(k)}
              {k.version === row.kvn && " · in use"}
            </span>
          ))}
        </Stack>
      ),
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
    width: 130,
    align: "right",
    headerAlign: "right",
    renderCell: ({ row }) => (
      <>
        <Tooltip title={newJobBlock(row, smscUnset) ?? "New job"} arrow>
          <span>
            <IconButton
              size="small"
              aria-label={`New job for ${row.iccid}`}
              onClick={() => onNewJob(row)}
              disabled={newJobBlock(row, smscUnset) !== undefined}
            >
              <NewJobIcon
                fontSize="small"
                color={
                  newJobBlock(row, smscUnset) === undefined
                    ? "primary"
                    : "disabled"
                }
              />
            </IconButton>
          </span>
        </Tooltip>
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
  const [newJob, setNewJob] = useState<Card | null>(null);

  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: PAGE_SIZE_OPTIONS[0],
  });

  const cards = useQuery({
    queryKey: ["cards", pagination],
    queryFn: () =>
      listCards({ page: pagination.page + 1, perPage: pagination.pageSize }),
    placeholderData: (prev) => prev,
  });
  const { data, isFetching } = cards;
  const smscUnset = useQuery(smscQuery).data?.password_set === false;

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
        count={data?.total_count}
        description="The SIM cards this platform manages over the air, and their OTA keys."
        action={
          <Stack direction="row" spacing={1} sx={{ flexShrink: 0 }}>
            <Button variant="outlined" onClick={() => setImporting(true)}>
              Import
            </Button>
            <Button
              variant="contained"
              color="success"
              onClick={() => setAdding(true)}
            >
              Add
            </Button>
          </Stack>
        }
      />
      <QueryAlert query={cards} subject="cards" />
      <DataGrid<Card>
        aria-label="Cards"
        rows={data?.items ?? []}
        columns={columns(smscUnset, setNewJob, setEditing, setDeleting)}
        getRowId={(card) => card.iccid}
        loading={isFetching && !data}
        paginationMode="server"
        rowCount={data?.total_count ?? 0}
        paginationModel={pagination}
        onPaginationModelChange={setPagination}
        pageSizeOptions={PAGE_SIZE_OPTIONS}
        disableColumnMenu
        initialState={{ columns: { columnVisibilityModel: hiddenByDefault } }}
        disableColumnSorting
        disableColumnFilter
        disableRowSelectionOnClick
        disableVirtualization
        autoHeight
        localeText={{ noRowsLabel: "No cards." }}
        getRowHeight={() => "auto"}
        sx={{
          ...GRID_SX,
          "& .MuiDataGrid-cell": {
            ...GRID_SX["& .MuiDataGrid-cell"],
            py: 1,
          },
        }}
      />
      {adding && <AddCardDialog onClose={() => setAdding(false)} />}
      {importing && <ImportCardsDialog onClose={() => setImporting(false)} />}
      {editing && (
        <EditCardDialog card={editing} onClose={() => setEditing(null)} />
      )}
      {newJob && (
        <NewJobDialog iccids={[newJob.iccid]} onClose={() => setNewJob(null)} />
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
