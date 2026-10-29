export const MAX_WIDTH = 1400;

export const PAGE_PADDING_X = { xs: 2, sm: 3, md: 4, lg: 5 };

// DATE_WIDTH fits a data grid's date, of 19 characters as YYYY-MM-DD HH:MM:SS: 160px in Source Code Pro at 14px,
// with the 10px padding each side of a cell, and some room.
export const DATE_WIDTH = 190;

// GRID_SX frames a data grid on a page, as a table is.
export const GRID_SX = {
  width: "100%",
  border: 1,
  borderColor: "divider",
  bgcolor: "background.paper",
  "& .MuiDataGrid-cell": {
    borderBottom: "1px solid",
    borderColor: "divider",
  },
  "& .MuiDataGrid-columnHeaders": {
    borderBottom: "1px solid",
    borderColor: "divider",
  },
} as const;

export const TABLE_CONTAINER_SX = {
  border: 1,
  borderColor: "divider",
  borderRadius: 1,
  bgcolor: "background.paper",
} as const;
