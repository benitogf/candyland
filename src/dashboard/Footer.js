import React from 'react'
import Box from '@mui/material/Box'
import Link from '@mui/material/Link'
import Typography from '@mui/material/Typography'

// Slim status footer. Once the orchestrator is connected this is where the
// live server clock / connection state goes (see benitogf/mono for the pattern).
const Footer = () => (
    <Box
        component="footer"
        sx={{
            height: 40,
            px: 2,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            borderTop: '1px solid',
            borderColor: 'divider',
            backgroundColor: 'rgba(20, 13, 31, 0.6)',
            backdropFilter: 'blur(8px)',
        }}
    >
        <Typography variant="caption" color="text.secondary">
            🍬 Candyland — solo agent orchestration
        </Typography>
        <Link
            href="https://github.com/benitogf/candyland"
            target="_blank"
            rel="noreferrer"
            variant="caption"
            color="text.secondary"
            underline="hover"
        >
            benitogf/candyland
        </Link>
    </Box>
)

export default Footer
