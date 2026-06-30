import React from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import QuestWorkspace from './QuestWorkspace'
import CampaignWorkspace from './CampaignWorkspace'

// Overlay host for a quest/campaign detail, layered over the work section. Close
// returns to the previous URL (navigate(-1)) so the work section's active level
// + filters are preserved — falling back to the work section if there is no
// history (e.g. a deep link opened directly).
const WorkDetail = ({ kind }) => {
    const navigate = useNavigate()
    const { id } = useParams()
    const close = () => {
        if (window.history.length > 1) navigate(-1)
        else navigate('/tasks')
    }
    if (kind === 'quest') return <QuestWorkspace id={id} onClose={close} />
    return <CampaignWorkspace id={id} onClose={close} />
}

export default WorkDetail
