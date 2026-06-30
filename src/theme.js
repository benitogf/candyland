import { createTheme } from '@mui/material/styles'
import { candy } from './config'

// Candyland runs on a near-black canvas with neon candy accents. The dominant
// accent (primary) is cyan.
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
        typography: {
            h3: { fontWeight: 800, letterSpacing: '-0.02em' },
            h4: { fontWeight: 800, letterSpacing: '-0.01em' },
            h5: { fontWeight: 700 },
            h6: { fontWeight: 700 },
            overline: { letterSpacing: '0.12em' },
        },
        components: {
            MuiPaper: { styleOverrides: { root: { backgroundImage: 'none' } } },
            MuiCard: {
                styleOverrides: {
                    root: { border: `1px solid ${candy.line}`, backgroundColor: candy.bgPaper },
                },
            },
        },
    })

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
