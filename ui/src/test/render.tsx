import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { ThemeProvider } from "@mui/material/styles";
import { SnackbarProvider } from "@/contexts/SnackbarContext";
import theme, { THEME_PROVIDER_PROPS } from "@/utils/theme";

// renderWithClient renders ui with a query client that does not retry. The theme mode is not stored.
export const renderWithClient = (ui: ReactElement) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return {
    client,
    ...render(
      <ThemeProvider
        theme={theme}
        {...THEME_PROVIDER_PROPS}
        storageManager={null}
      >
        <QueryClientProvider client={client}>
          <SnackbarProvider>{ui}</SnackbarProvider>
        </QueryClientProvider>
      </ThemeProvider>,
    ),
  };
};

export const stubApi = (
  routes: Record<string, (url: URL, init?: RequestInit) => Response>,
) => {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://simota.test");
    const route = routes[url.pathname];
    if (!route) {
      return new Response(JSON.stringify({ error: "not found" }), {
        status: 404,
      });
    }
    return route(url, init);
  };
};

export const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status });
