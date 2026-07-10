import React from 'react'
import { useNavigate, useParams } from 'react-router-dom'

import QuestWorkspace from './QuestWorkspace'

// Overlay host for a quest detail, layered over the work section. Close returns
// to the previous URL (navigate(-1)) so the work section's active level +
// filters are preserved — falling back to the work section if there is no
// history (e.g. a deep link opened directly).
const WorkDetail = () => {
    const navigate = useNavigate()
    const { id } = useParams()
    const close = () => {
        if (window.history.length > 1) navigate(-1)
        else navigate('/tasks')
    }
    return <QuestWorkspace id={id} onClose={close} />
}

export default WorkDetail
