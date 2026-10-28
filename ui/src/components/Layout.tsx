import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  AppBar,
  Box,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  ListSubheader,
  Menu,
  MenuItem,
  Toolbar,
  Typography,
  useMediaQuery,
} from "@mui/material";
import { useColorScheme, useTheme } from "@mui/material/styles";
import {
  Brightness6 as ThemeIcon,
  BugReport as BugReportIcon,
  Check as CheckIcon,
  DarkMode as DarkModeIcon,
  LightMode as LightModeIcon,
  Menu as MenuIcon,
  OpenInNew as OpenInNewIcon,
  SimCard as SimCardIcon,
} from "@mui/icons-material";
import { Link, useLocation } from "react-router-dom";
import DeploymentIdentity from "@/components/DeploymentIdentity";
import Footer from "@/components/Footer";
import Logo from "@/components/Logo";
import { MAX_WIDTH, PAGE_PADDING_X } from "@/utils/layout";

const drawerWidth = 250;

const NEW_ISSUE_URL = "https://github.com/ellanetworks/simota/issues/new";

const drawerSelectedSx = {
  "& .MuiListItemText-primary": { color: "primary.main" },

  "&:hover": { bgcolor: "transparent" },
  "&.Mui-selected": { bgcolor: "transparent" },
  "&.Mui-selected:hover": { bgcolor: "transparent" },

  "&.Mui-selected .MuiListItemText-primary": {
    fontWeight: 700,
    textDecoration: "underline",
    textDecorationColor: "primary.main",
    textUnderlineOffset: "4px",
    textDecorationThickness: "2px",
  },

  "&:hover .MuiListItemText-primary": {
    textDecoration: "underline",
    textDecorationColor: "primary.main",
    textUnderlineOffset: "4px",
    textDecorationThickness: "2px",
  },
};

const THEME_MODES = [
  { value: "system", label: "System", Icon: ThemeIcon },
  { value: "light", label: "Light", Icon: LightModeIcon },
  { value: "dark", label: "Dark", Icon: DarkModeIcon },
] as const;

interface NavItem {
  to: string;
  label: string;
  icon: ReactNode;
}

const navItems: NavItem[] = [
  {
    to: "/cards",
    label: "Cards",
    icon: <SimCardIcon color="primary" />,
  },
];

export default function Layout({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const [themeEl, setThemeEl] = useState<HTMLElement | null>(null);
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down("lg"));
  const [mobileOpen, setMobileOpen] = useState(false);
  const { mode, setMode } = useColorScheme();

  const isFirstRender = useRef(true);
  useEffect(() => {
    if (isFirstRender.current) {
      isFirstRender.current = false;
      return;
    }
    document.getElementById("main-content")?.focus();
    // oxlint-disable-next-line react/exhaustive-effect-dependencies
  }, [pathname]);

  const handleNavClick = () => {
    if (isMobile) setMobileOpen(false);
  };

  return (
    <Box sx={{ display: "flex" }}>
      <Box
        component="a"
        href="#main-content"
        onClick={(e: React.MouseEvent<HTMLAnchorElement>) => {
          e.preventDefault();
          document.getElementById("main-content")?.focus();
        }}
        sx={{
          position: "absolute",
          left: "-9999px",
          top: "auto",
          width: "1px",
          height: "1px",
          overflow: "hidden",
          zIndex: (t) => t.zIndex.modal + 1,
          "&:focus": {
            position: "fixed",
            top: 8,
            left: 8,
            width: "auto",
            height: "auto",
            overflow: "visible",
            bgcolor: "background.paper",
            color: "primary.main",
            px: 2,
            py: 1,
            borderRadius: 1,
            boxShadow: 3,
            fontWeight: 700,
            textDecoration: "none",
          },
        }}
      >
        Skip to main content
      </Box>
      <AppBar position="fixed" sx={{ zIndex: (t) => t.zIndex.drawer + 1 }}>
        <Toolbar>
          {isMobile && (
            <IconButton
              color="inherit"
              aria-label="open drawer"
              edge="start"
              onClick={() => setMobileOpen(!mobileOpen)}
              sx={{ mr: 2 }}
            >
              <MenuIcon />
            </IconButton>
          )}
          <Logo />
          <Typography variant="h6" noWrap component="div" sx={{ ml: 2 }}>
            Ella SIM OTA
          </Typography>
          <Box sx={{ flexGrow: 1 }} />
          <Box sx={{ mr: 2, minWidth: 0 }}>
            <DeploymentIdentity />
          </Box>
          <IconButton
            size="large"
            edge="end"
            color="inherit"
            aria-label="Theme menu"
            aria-haspopup="menu"
            aria-expanded={themeEl !== null}
            aria-controls={themeEl !== null ? "theme-menu" : undefined}
            onClick={(e) => setThemeEl(e.currentTarget)}
          >
            <ThemeIcon />
          </IconButton>
          <Menu
            id="theme-menu"
            anchorEl={themeEl}
            open={themeEl !== null}
            onClose={() => setThemeEl(null)}
            anchorOrigin={{ vertical: "bottom", horizontal: "right" }}
            transformOrigin={{ vertical: "top", horizontal: "right" }}
          >
            <ListSubheader disableSticky role="presentation">
              Theme
            </ListSubheader>
            {/* oxlint-disable-next-line jsx-a11y/prefer-tag-over-role */}
            <li role="group" aria-label="Theme">
              {THEME_MODES.map(({ value, label, Icon }) => (
                <MenuItem
                  key={value}
                  component="div"
                  role="menuitemradio"
                  aria-label={`${label} theme`}
                  aria-checked={mode === value}
                  selected={mode === value}
                  onClick={() => {
                    setMode(value);
                    setThemeEl(null);
                  }}
                >
                  <ListItemIcon>
                    <Icon fontSize="small" color="primary" />
                  </ListItemIcon>
                  <ListItemText primary={label} />
                  {mode === value && (
                    <CheckIcon fontSize="small" sx={{ ml: 2 }} />
                  )}
                </MenuItem>
              ))}
            </li>
          </Menu>
        </Toolbar>
      </AppBar>

      <Drawer
        variant={isMobile ? "temporary" : "permanent"}
        open={isMobile ? mobileOpen : true}
        onClose={() => setMobileOpen(false)}
        ModalProps={{ keepMounted: true }}
        sx={{
          "& .MuiDrawer-paper": {
            width: drawerWidth,
            boxSizing: "border-box",
            display: "flex",
            flexDirection: "column",
          },
        }}
      >
        <Toolbar />
        <Box
          component="nav"
          aria-label="Main"
          sx={{ flexGrow: 1, overflow: "auto" }}
        >
          <List>
            {navItems.map((item) => (
              <NavLink
                key={item.to}
                item={item}
                pathname={pathname}
                onClick={handleNavClick}
              />
            ))}
          </List>
        </Box>

        <Divider />
        <Box>
          <List>
            <ListSubheader>Support</ListSubheader>
            <ListItem disablePadding>
              <ListItemButton
                component="a"
                href={NEW_ISSUE_URL}
                target="_blank"
                rel="noreferrer"
                onClick={handleNavClick}
                sx={drawerSelectedSx}
              >
                <ListItemIcon>
                  <BugReportIcon color="primary" />
                </ListItemIcon>
                <ListItemText primary="Report a bug" />
                <OpenInNewIcon
                  sx={{ fontSize: 16, ml: 1, color: "action.active" }}
                />
              </ListItemButton>
            </ListItem>
          </List>
        </Box>
      </Drawer>
      <Box
        component="main"
        id="main-content"
        tabIndex={-1}
        sx={{
          outline: "none",
          flexGrow: 1,
          minWidth: 0,
          ml: isMobile ? 0 : `${drawerWidth}px`,
          minHeight: "100vh",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <Toolbar />
        <Box
          sx={{
            flexGrow: 1,
            minWidth: 0,
            width: "100%",
            pt: 6,
            pb: 4,
            maxWidth: MAX_WIDTH,
            mx: "auto",
            px: PAGE_PADDING_X,
          }}
        >
          {children}
        </Box>
        <Footer />
      </Box>
    </Box>
  );
}

function NavLink({
  item: { to, label, icon },
  pathname,
  onClick,
}: {
  item: NavItem;
  pathname: string;
  onClick: () => void;
}) {
  const current = pathname.startsWith(to);

  return (
    <ListItem disablePadding>
      <ListItemButton
        component={Link}
        to={to}
        selected={current}
        aria-current={current ? "page" : undefined}
        onClick={onClick}
        sx={drawerSelectedSx}
      >
        <ListItemIcon>{icon}</ListItemIcon>
        <ListItemText primary={label} />
      </ListItemButton>
    </ListItem>
  );
}
