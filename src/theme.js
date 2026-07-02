import { createTheme } from '@mui/material/styles'
import { candy } from './config'

// The shared font stack. Kept in one place so every view — and the Mermaid
// diagrams (via `fontFamily: 'inherit'`) — render with the same typeface.
const fontStack =
    '"Inter", "Segoe UI", system-ui, -apple-system, "Helvetica Neue", Arial, sans-serif'
const monoStack =
    '"JetBrains Mono", "SFMono-Regular", Menlo, Consolas, "Liberation Mono", monospace'

// Candyland runs on a near-black canvas with neon candy accents. The dominant
// accent (primary) is cyan.
//
// This theme is the single source of truth for spacing, typography, and visual
// hierarchy across the dashboard, quest, campaign, and task-run views. The MUI
// spacing unit is the default 8px; views compose spacing from that grid (mb: 2
// = 16px, etc.) and inherit the type scale and component defaults defined here
// rather than restyling ad hoc.
export const makeTheme = () =>
    createTheme({
        palette: {
            mode: 'dark',
            primary: { main: candy.sky },
            secondary: { main: candy.mint },
            info: { main: candy.sky },
            warning: { main: candy.lemon },
            success: { main: candy.mint },
            background: {
                default: candy.bgDark,
                paper: candy.bgPaper,
            },
            divider: candy.line,
        },
        shape: { borderRadius: 14 },
        // A deliberate, compact type scale. Display headings (h3/h4) are heavy
        // and tight; section headings (h5/h6) step down in weight; body copy and
        // metadata (body2/caption/overline) share consistent line-height and
        // spacing so hierarchy reads the same in every view.
        typography: {
            fontFamily: fontStack,
            h1: { fontWeight: 800, letterSpacing: '-0.03em', lineHeight: 1.1 },
            h2: { fontWeight: 800, letterSpacing: '-0.02em', lineHeight: 1.15 },
            h3: { fontWeight: 800, letterSpacing: '-0.02em', lineHeight: 1.2 },
            h4: { fontWeight: 800, letterSpacing: '-0.01em', lineHeight: 1.25 },
            h5: { fontWeight: 700, letterSpacing: '-0.01em', lineHeight: 1.3 },
            h6: { fontWeight: 700, lineHeight: 1.35 },
            subtitle1: { fontWeight: 600, lineHeight: 1.4 },
            subtitle2: { fontWeight: 600, lineHeight: 1.4 },
            body1: { lineHeight: 1.6 },
            body2: { lineHeight: 1.55 },
            caption: { lineHeight: 1.45, letterSpacing: '0.01em' },
            overline: { fontWeight: 700, letterSpacing: '0.12em', lineHeight: 1.5 },
            button: { fontWeight: 700, letterSpacing: '0.01em' },
        },
        components: {
            MuiPaper: { styleOverrides: { root: { backgroundImage: 'none' } } },
            MuiCard: {
                defaultProps: { elevation: 0 },
                styleOverrides: {
                    root: {
                        border: `1px solid ${candy.line}`,
                        backgroundColor: candy.bgPaper,
                        padding: 16,
                    },
                },
            },
            // Buttons and chips: keep the type upright (no shouty uppercase) and
            // consistently rounded, so actions read the same everywhere.
            MuiButton: {
                defaultProps: { disableElevation: true },
                styleOverrides: {
                    root: { textTransform: 'none', borderRadius: 10 },
                    sizeSmall: { paddingTop: 4, paddingBottom: 4 },
                },
            },
            MuiChip: {
                styleOverrides: {
                    root: { fontWeight: 600 },
                    label: { letterSpacing: '0.01em' },
                },
            },
            MuiLink: { defaultProps: { underline: 'hover' } },
            MuiTooltip: {
                styleOverrides: {
                    tooltip: { backgroundColor: candy.bgPaperHi, border: `1px solid ${candy.line}`, fontSize: 12 },
                },
            },
        },
    })

// Shared monospace stack for code / live-output wells, exported so views can
// reference the same typeface instead of hardcoding 'monospace'.
export const monoFontFamily = monoStack

// Mermaid theme variables, derived from the palette so diagrams match the UI.
export const mermaidTheme = {
    background: candy.bgPaper,
    primaryColor: candy.bgPaperHi,
    primaryTextColor: '#f4ecff',
    primaryBorderColor: candy.grape,
    lineColor: candy.sky,
    secondaryColor: '#1c1c22',
    tertiaryColor: '#141418',
    fontFamily: 'inherit',
    fontSize: '15px',
    actorBkg: candy.bgPaperHi,
    actorBorder: candy.pink,
    actorTextColor: '#f4ecff',
    signalColor: '#cfe9ff',
    signalTextColor: '#cfe9ff',
    labelBoxBkgColor: candy.bgPaperHi,
    noteBkgColor: '#1c1c22',
    noteTextColor: '#f4ecff',
}

export default makeTheme
