import React from 'react'
import Box from '@mui/material/Box'
import Card from '@mui/material/Card'
import Chip from '@mui/material/Chip'
import Typography from '@mui/material/Typography'

// A titled content block used throughout the "How it works" page.
export const Section = ({ kicker, title, intro, children }) => (
    <Box component="section" sx={{ mb: 6 }}>
        {kicker && (
            <Typography variant="overline" color="secondary" sx={{ display: 'block', mb: 0.5 }}>
                {kicker}
            </Typography>
        )}
        <Typography variant="h4" sx={{ mb: intro ? 1 : 2 }}>
            {title}
        </Typography>
        {intro && (
            <Typography variant="body1" color="text.secondary" sx={{ maxWidth: 820, mb: 2 }}>
                {intro}
            </Typography>
        )}
        {children}
    </Box>
)

// A framed canvas for a diagram, with an optional caption.
export const DiagramCard = ({ caption, children }) => (
    <Card sx={{ p: { xs: 1.5, sm: 3 }, overflowX: 'auto' }}>
        {children}
        {caption && (
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1, textAlign: 'center' }}>
                {caption}
            </Typography>
        )}
    </Card>
)

// A "spec note" callout — this dashboard is the spec, so these mark the
// concrete contract for the eventual implementation.
export const SpecNote = ({ children }) => (
    <Box
        sx={{
            mt: 2,
            p: 2,
            borderRadius: 2,
            borderLeft: '3px solid',
            borderColor: 'warning.main',
            backgroundColor: 'rgba(255, 217, 61, 0.06)',
        }}
    >
        <Chip label="📐 spec" size="small" color="warning" variant="outlined" sx={{ mb: 1, fontWeight: 700 }} />
        <Typography variant="body2" color="text.secondary">
            {children}
        </Typography>
    </Box>
)

export default Section
