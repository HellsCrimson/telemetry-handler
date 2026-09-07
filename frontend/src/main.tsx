import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

// Fonts are bundled rather than fetched from Google: this is a desktop app at a
// sim rig, and a webview that falls back to system sans because the network is
// down would break the whole type scale.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import '@fontsource/ibm-plex-mono/600.css'

// The design system loads before anything else so tokens exist for every
// component that follows. style.css is the legacy sheet, still carrying the
// tabs that have not been converted yet.
import './design/tokens.css'
import './design/components.css'
import './design/controls.css'

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
