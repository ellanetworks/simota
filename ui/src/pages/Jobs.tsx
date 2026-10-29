import { useCallback, useState } from "react";
import { Box, Button, Stack, TextField, Tooltip } from "@mui/material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import JobDrawer from "@/components/JobDrawer";
import NewJobDialog from "@/components/NewJobDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { JobStatusChip } from "@/components/chips";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import { type Job, listJobs } from "@/queries/jobs";
import { smscQuery } from "@/queries/smsc";
import { formatTimestamp } from "@/utils/dates";
import { jobLabel, porLabel } from "@/utils/jobs";
import { DATE_WIDTH, GRID_SX } from "@/utils/layout";

export const JOBS_REFRESH_MS = 5000;

const FILTER_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const columns: GridColDef<Job>[] = [
  {
    field: "created_at",
    headerName: "Created",
    width: DATE_WIDTH,
    valueFormatter: (value: string) => formatTimestamp(value),
  },
  { field: "iccid", headerName: "Card", width: 200 },
  // The text columns of variable length share the spare width, so that none is cut while there is room.
  {
    field: "operation",
    headerName: "Operation",
    flex: 2,
    minWidth: 220,
    valueGetter: (_value, row) => jobLabel(row),
  },
  {
    field: "status",
    headerName: "Status",
    width: 130,
    renderCell: ({ row }) => <JobStatusChip status={row.status} />,
  },
  {
    field: "por_status",
    headerName: "PoR",
    flex: 1,
    minWidth: 170,
    valueGetter: (_value, row) => porLabel(row.por_status),
  },
  {
    field: "updated_at",
    headerName: "Updated",
    width: DATE_WIDTH,
    valueFormatter: (value: string) => formatTimestamp(value),
  },
];

export default function Jobs() {
  const [search, setSearch] = useState("");
  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: PAGE_SIZE_OPTIONS[0],
  });
  const [selected, setSelected] = useState<Job | null>(null);
  const closeDrawer = useCallback(() => setSelected(null), []);
  const [creating, setCreating] = useState(false);

  const debouncedSearch = useDebouncedValue(search, FILTER_DEBOUNCE_MS);

  const jobs = useQuery({
    queryKey: ["jobs", debouncedSearch, pagination],
    queryFn: () =>
      listJobs({
        search: debouncedSearch,
        page: pagination.page + 1,
        perPage: pagination.pageSize,
      }),
    placeholderData: (prev) => prev,
    refetchInterval: JOBS_REFRESH_MS,
  });
  const { data, isFetching } = jobs;
  // Jobs wait for an SMSC: creating one needs it set.
  const smscUnset = useQuery(smscQuery).data?.password_set === false;

  return (
    <Box component="section" aria-labelledby="jobs-title">
      <PageHeader
        id="jobs-title"
        title="Jobs"
        description="The Remote File Management operations sent to the cards over the air, newest first."
        action={
          <Tooltip title={smscUnset ? "Set the SMSC first" : ""} arrow>
            <span>
              <Button
                variant="contained"
                color="success"
                onClick={() => setCreating(true)}
                disabled={smscUnset}
              >
                New Job
              </Button>
            </span>
          </Tooltip>
        }
      />
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={2}
        useFlexGap
        sx={{ mb: 2, flexWrap: "wrap" }}
      >
        <TextField
          label="Search"
          size="small"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value.trim());
            setPagination((p) => ({ ...p, page: 0 }));
          }}
          placeholder="ICCID, IMSI or MSISDN"
          sx={{ minWidth: 260 }}
        />
      </Stack>
      <QueryAlert query={jobs} subject="jobs" />
      <DataGrid<Job>
        aria-label="Jobs"
        density="compact"
        rows={data?.items ?? []}
        columns={columns}
        rowCount={data?.total_count ?? 0}
        loading={data === undefined && isFetching}
        paginationMode="server"
        paginationModel={pagination}
        onPaginationModelChange={setPagination}
        pageSizeOptions={PAGE_SIZE_OPTIONS}
        onRowClick={({ row }) => setSelected(row)}
        onCellKeyDown={({ row }, event) => {
          if (event.key === "Enter") setSelected(row);
        }}
        disableColumnSorting
        disableColumnFilter
        disableRowSelectionOnClick
        hideFooterSelectedRowCount
        rowSelectionModel={{
          type: "include",
          ids: new Set(selected ? [selected.id] : []),
        }}
        disableVirtualization
        autoHeight
        localeText={{
          noRowsLabel: debouncedSearch ? "No jobs match." : "No jobs.",
        }}
        sx={{
          ...GRID_SX,
          "& .MuiDataGrid-row": { cursor: "pointer" },
          "& .MuiDataGrid-row.Mui-selected": {
            backgroundColor: (t) => t.palette.action.selected,
            "&:hover": { backgroundColor: (t) => t.palette.action.selected },
            "& .MuiDataGrid-cell": { fontWeight: 500 },
          },
        }}
      />
      <JobDrawer job={selected} onClose={closeDrawer} />
      {creating && <NewJobDialog onClose={() => setCreating(false)} />}
    </Box>
  );
}
